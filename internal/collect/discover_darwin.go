//go:build darwin

package collect

import "github.com/AndiOliverIon/meerkat-agent/internal/model"

// Resource discovery is not yet implemented on Darwin; a dedicated backend
// (launchd services, Docker Desktop/OrbStack, on-disk DB sizing) is future
// work. These stubs return nil ("not obtained" -> JSON null) rather than empty
// slices, since on Darwin we genuinely did not collect this data (vs.
// "collected, found none").

func readContainers() []model.Container                { return nil }
func readDatabases([]model.Container) []model.Database { return nil }
func readEndpoints() []model.Endpoint                  { return nil }
