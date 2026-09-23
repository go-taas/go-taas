package server

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubMigratorService is a Service that also implements Migrator.
type stubMigratorService struct {
	stubService
	migrated bool
	err      error
}

func (s *stubMigratorService) Migrate(_ context.Context) error {
	s.migrated = true
	return s.err
}

func TestInitRunsMigrators(t *testing.T) {
	srv, err := NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2})
	require.NoError(t, err)

	m := &stubMigratorService{}
	srv.RegisterService(m)

	// Init without a loaded configuration: components are disabled, but
	// the Migrator hook must still run for every registered service.
	srv.Init()
	assert.True(t, m.migrated, "Migrator hook must run during Init")
}

func TestInitAbortsOnMigrationFailure(t *testing.T) {
	if os.Getenv("GO_TAAS_TEST_MIGRATION_FAILURE") == "1" {
		srv, err := NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2})
		require.NoError(t, err)
		srv.RegisterService(&stubMigratorService{err: assert.AnError})
		srv.Init()
		os.Exit(0) //nolint:revive // subprocess exit is the point of this test
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestInitAbortsOnMigrationFailure$")
	cmd.Env = append(os.Environ(), "GO_TAAS_TEST_MIGRATION_FAILURE=1")
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "migration failure must abort startup; output: %s", output)
	assert.Equal(t, 1, exitErr.ExitCode(), "unexpected child output: %s", output)
}

func TestComponentsShellBeforeInit(t *testing.T) {
	// Regression: Components() captured before Init must be usable after
	// Init populates the shell (previously it wrapped a nil pointer and
	// panicked on first use).
	srv, err := NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2})
	require.NoError(t, err)

	components := srv.Components()
	require.NotNil(t, components, "Components() must return a non-nil shell before Init")

	srv.Init()

	// Calling through the captured interface must not panic: with no
	// configuration loaded all components are disabled and return nil.
	assert.NotPanics(t, func() {
		assert.Nil(t, components.DB())
		assert.Nil(t, components.Redis())
		assert.Nil(t, components.MQ())
	})
}
