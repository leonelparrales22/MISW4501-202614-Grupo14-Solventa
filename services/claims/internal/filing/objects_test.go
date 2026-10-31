package filing_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// objectStore is the object storage of the tests: what a client would have
// uploaded with the signed URL, by key.
type objectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	// digest is what Stat reports as the SHA-256 the storage computed ("" is a
	// storage that computes none).
	digest map[string]string

	signErr, statErr, headErr error
	signed                    []string
}

func newObjectStore() *objectStore {
	return &objectStore{objects: map[string][]byte{}, digest: map[string]string{}}
}

// put is the upload of the client.
func (o *objectStore) put(key string, content []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.objects[key] = content
}

func (o *objectStore) SignUpload(_ context.Context, key string, _ domain.Upload, _ time.Duration) (ports.SignedUpload, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.signErr != nil {
		return ports.SignedUpload{}, o.signErr
	}
	o.signed = append(o.signed, key)
	return ports.SignedUpload{
		URL: "https://bucket.test/" + key + "?sig=1", Headers: map[string]string{"Content-Type": "x"},
		ExpiresAt: filedAt.Add(filing.SupportUploadTTL),
	}, nil
}

func (o *objectStore) Stat(_ context.Context, key string) (ports.StoredEvidence, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.statErr != nil {
		return ports.StoredEvidence{}, o.statErr
	}
	content, ok := o.objects[key]
	if !ok {
		return ports.StoredEvidence{}, ports.ErrEvidenceNotFound
	}
	return ports.StoredEvidence{ByteSize: int64(len(content)), SHA256: o.digest[key]}, nil
}

func (o *objectStore) Head(_ context.Context, key string, n int64) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.headErr != nil {
		return nil, o.headErr
	}
	content, ok := o.objects[key]
	if !ok {
		return nil, ports.ErrEvidenceNotFound
	}
	if int64(len(content)) < n {
		n = int64(len(content))
	}
	return content[:n], nil
}

func (o *objectStore) SignDownload(_ context.Context, key string, ttl time.Duration) (ports.SignedDownload, error) {
	if o.signErr != nil {
		return ports.SignedDownload{}, errors.New("cannot sign")
	}
	return ports.SignedDownload{URL: "https://bucket.test/" + key + "?get=1", ExpiresAt: filedAt.Add(ttl)}, nil
}
