package main

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
)

// HU-W3.2-01: the summary of the partner dashboard reads PostgreSQL, so it is
// mounted only with a database.
func TestThePartnerDashboardNeedsTheDatabase(t *testing.T) {
	assert.Empty(t, partnerMetricsOptions(nil, nil))
	assert.Len(t, partnerMetricsOptions(&sql.DB{}, nil), 1)
}
