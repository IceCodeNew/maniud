package imagearchive_test

import (
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/imagearchive"
)

func TestServiceNameUsesArchiveReference(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, fixtureOptions{architecture: testArchitectureAMD64})
	analysis := analyzeFixture(t, fixture, testArchiveTag)
	name, err := analysis.ServiceName("")
	if err != nil {
		t.Fatalf("ServiceName() error = %v", err)
	}
	if name != "app" {
		t.Fatalf("ServiceName() = %q", name)
	}
}

func TestServiceNameUsesExplicitNameAndRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, fixtureOptions{architecture: testArchitectureARM64, variant: "v8"})
	analysis := analyzeFixture(t, fixture, "@0")
	name, err := analysis.ServiceName("worker-1")
	if err != nil || name != "worker-1" {
		t.Fatalf("ServiceName(explicit) = %q, %v", name, err)
	}
	if _, err = analysis.ServiceName("Invalid_Name"); !errors.Is(err, imagearchive.ErrInvalidArchive) {
		t.Fatalf("ServiceName(invalid name) error = %v", err)
	}
	if _, err = (imagearchive.Analysis{}).ServiceName(""); !errors.Is(err, imagearchive.ErrInvalidArchive) {
		t.Fatalf("ServiceName(empty) error = %v", err)
	}
}
