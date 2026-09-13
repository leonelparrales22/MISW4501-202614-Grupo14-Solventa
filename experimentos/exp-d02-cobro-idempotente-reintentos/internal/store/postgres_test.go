// Pruebas de integración contra un Postgres real, levantado efímero por
// Testcontainers-Go (mismo patrón recomendado en frameworks.md del
// proyecto). Requieren Docker disponible en el entorno donde corre
// `go test`.
package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/domain"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/store"
)

// levantarDB arranca un contenedor Postgres efímero con db/schema.sql ya
// aplicado (el mismo archivo que usa docker-compose.yml, para que el
// esquema de test y el de compose nunca diverjan) y devuelve un *sql.DB
// conectado a él. El contenedor se destruye al terminar el test.
func levantarDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	schemaPath, err := filepath.Abs(filepath.Join("..", "..", "db", "schema.sql"))
	require.NoError(t, err)
	if _, err := os.Stat(schemaPath); err != nil {
		t.Fatalf("no se encontró db/schema.sql en %s: %v", schemaPath, err)
	}

	pgContainer, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:16-alpine"),
		postgres.WithDatabase("cobros"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.WithInitScripts(schemaPath),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second)),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pgContainer.Terminate(context.Background())
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.Eventually(t, func() bool {
		return db.PingContext(ctx) == nil
	}, 15*time.Second, 200*time.Millisecond)

	return db
}

func crearAceptacion(t *testing.T, s *store.Store, id string) {
	t.Helper()
	err := s.CrearAceptacion(context.Background(), domain.Aceptacion{
		ID: id, ClienteID: "cliente-x", MontoCentavos: 15000, Moneda: "COP",
	})
	require.NoError(t, err)
}

func TestCrearIntentoPendiente_EsIdempotente(t *testing.T) {
	db := levantarDB(t)
	s := store.New(db)
	ctx := context.Background()

	aceptacionID := "11111111-1111-1111-1111-111111111111"
	crearAceptacion(t, s, aceptacionID)
	key := domain.IdempotencyKeyPara(aceptacionID)

	primero, err := s.CrearIntentoPendiente(ctx, aceptacionID, key)
	require.NoError(t, err)

	segundo, err := s.CrearIntentoPendiente(ctx, aceptacionID, key)
	require.NoError(t, err)

	assert.Equal(t, primero.ID, segundo.ID)

	var total int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM intento_cobro WHERE aceptacion_id = $1`, aceptacionID,
	).Scan(&total))
	assert.Equal(t, 1, total)
}

// TestCrearIntentoPendiente_ConcurrenciaReal es la validación más
// importante del experimento a nivel de base de datos: N goroutines
// creando el intento para la MISMA aceptación al mismo tiempo, contra un
// Postgres real (no un fake), nunca deben producir más de una fila.
func TestCrearIntentoPendiente_ConcurrenciaReal(t *testing.T) {
	db := levantarDB(t)
	s := store.New(db)
	ctx := context.Background()

	aceptacionID := "22222222-2222-2222-2222-222222222222"
	crearAceptacion(t, s, aceptacionID)
	key := domain.IdempotencyKeyPara(aceptacionID)

	const n = 20
	var wg sync.WaitGroup
	barrera := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrera // todas arrancan al mismo tiempo
			_, err := s.CrearIntentoPendiente(ctx, aceptacionID, key)
			errs[i] = err
		}(i)
	}
	close(barrera)
	wg.Wait()

	for _, err := range errs {
		assert.NoError(t, err)
	}

	var total int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM intento_cobro WHERE aceptacion_id = $1`, aceptacionID,
	).Scan(&total))
	assert.Equal(t, 1, total, "el UNIQUE de idempotency_key debe garantizar exactamente una fila pese a la concurrencia real")
}

// TestReclamarPendiente_ConcurrenciaReal es la corrección del ADR-01
// puesta a prueba directamente contra Postgres: N goroutines reclamando
// el MISMO intento "pendiente" al mismo tiempo deben producir
// exactamente un reclamado=true. Es la prueba equivalente, a nivel de
// store, de lo que scripts/concurrencia verifica a nivel de HTTP contra
// el stack completo.
func TestReclamarPendiente_ConcurrenciaReal(t *testing.T) {
	db := levantarDB(t)
	s := store.New(db)
	ctx := context.Background()

	aceptacionID := "66666666-6666-6666-6666-666666666666"
	crearAceptacion(t, s, aceptacionID)
	_, err := s.CrearIntentoPendiente(ctx, aceptacionID, domain.IdempotencyKeyPara(aceptacionID))
	require.NoError(t, err)

	const n = 20
	var wg sync.WaitGroup
	barrera := make(chan struct{})
	reclamados := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrera
			_, reclamado, err := s.ReclamarPendiente(ctx, aceptacionID)
			reclamados[i] = reclamado
			errs[i] = err
		}(i)
	}
	close(barrera)
	wg.Wait()

	totalReclamados := 0
	for i, err := range errs {
		require.NoError(t, err)
		if reclamados[i] {
			totalReclamados++
		}
	}
	assert.Equal(t, 1, totalReclamados, "exactamente un llamador concurrente debe ganar el claim atómico")
}

func TestUniqueConstraint_RechazaInsertDuplicadoCrudo(t *testing.T) {
	db := levantarDB(t)
	ctx := context.Background()

	aceptacionID := "33333333-3333-3333-3333-333333333333"
	_, err := db.ExecContext(ctx,
		`INSERT INTO aceptacion (id, cliente_id, monto_centavos) VALUES ($1, 'c', 1000)`, aceptacionID)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO intento_cobro (aceptacion_id, idempotency_key) VALUES ($1, 'clave-fija')`, aceptacionID)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO intento_cobro (aceptacion_id, idempotency_key) VALUES ($1, 'clave-fija')`, aceptacionID)
	require.Error(t, err)
	assert.True(t, store.EsViolacionDeUnicidad(err), "se esperaba error 23505 (unique_violation) de Postgres, se obtuvo: %v", err)
}

func TestIntentosPendientesParaReintentar_SoloVencidos(t *testing.T) {
	db := levantarDB(t)
	s := store.New(db)
	ctx := context.Background()

	vencidoID := "44444444-4444-4444-4444-444444444444"
	futuroID := "55555555-5555-5555-5555-555555555555"
	crearAceptacion(t, s, vencidoID)
	crearAceptacion(t, s, futuroID)

	vencido, err := s.CrearIntentoPendiente(ctx, vencidoID, domain.IdempotencyKeyPara(vencidoID))
	require.NoError(t, err)
	futuro, err := s.CrearIntentoPendiente(ctx, futuroID, domain.IdempotencyKeyPara(futuroID))
	require.NoError(t, err)

	require.NoError(t, s.MarcarFallidoReintentar(ctx, vencido.ID, time.Now().Add(-time.Minute), "ambiguo"))
	require.NoError(t, s.MarcarFallidoReintentar(ctx, futuro.ID, time.Now().Add(time.Hour), "ambiguo"))

	pendientes, err := s.IntentosPendientesParaReintentar(ctx, time.Now())
	require.NoError(t, err)

	ids := make([]string, 0, len(pendientes))
	for _, p := range pendientes {
		ids = append(ids, p.AceptacionID)
	}
	assert.Contains(t, ids, vencidoID)
	assert.NotContains(t, ids, futuroID)
}
