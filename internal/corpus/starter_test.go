package corpus

import (
	"context"
	"errors"
	"testing"
)

type failingStore struct{ Store }

func (failingStore) ListBlocks(context.Context, ListFilter) ([]PhraseBlock, error) {
	return nil, errors.New("store down")
}

// The starter corpus is a starting point: it seeds an empty corpus and never
// touches one that has anything in it.
func TestStarterProvisioner_SeedsOnlyAnEmptyCorpus(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil)
	provisioner := StarterProvisioner{Service: svc}

	seeded, err := provisioner.ProvisionStarterCorpus(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	want := len(StarterBlocks())
	if seeded != want {
		t.Fatalf("seeded = %d, want %d", seeded, want)
	}
	listed, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1"})
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}
	if len(listed.Items) != want {
		t.Fatalf("corpus = %d blocks, want %d", len(listed.Items), want)
	}

	// Second call: the learner owns blocks, so nothing more is added.
	again, err := provisioner.ProvisionStarterCorpus(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if again != 0 {
		t.Fatalf("seeded = %d on a non-empty corpus, want 0", again)
	}
	listAgain, _ := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1"})
	if len(listAgain.Items) != want {
		t.Fatalf("corpus grew to %d blocks", len(listAgain.Items))
	}

	// Another learner is unaffected by the first one's corpus.
	other, err := provisioner.ProvisionStarterCorpus(context.Background(), "user-2")
	if err != nil || other != want {
		t.Fatalf("user-2 seeded = %d err=%v, want %d", other, err, want)
	}
}

func TestStarterProvisioner_NoServiceAndStoreFailure(t *testing.T) {
	if seeded, err := (StarterProvisioner{}).ProvisionStarterCorpus(context.Background(), "u"); seeded != 0 || err != nil {
		t.Fatalf("nil service = %d, %v", seeded, err)
	}
	broken := StarterProvisioner{Service: NewService(failingStore{NewMemoryStore()}, nil)}
	if _, err := broken.ProvisionStarterCorpus(context.Background(), "u"); err == nil {
		t.Fatal("a store failure must surface")
	}
}
