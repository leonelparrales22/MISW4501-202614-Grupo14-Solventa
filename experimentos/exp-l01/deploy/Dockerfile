# Una sola imagen para EXP-L01. El mismo binario arranca como orquestador o
# como fuentes simuladas según el --mode que le pasen (ver docker-compose.yml).
#
# Primera etapa: compilar. El go mod tidy es para que genere el go.sum
# aunque no lo tengamos en el repo.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /expl01 ./cmd/expl01

# Segunda etapa: la imagen final. Distroless, sin shell ni nada, solo el binario.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /expl01 /expl01
USER nonroot:nonroot
ENTRYPOINT ["/expl01"]
CMD ["--mode=orquestador"]
