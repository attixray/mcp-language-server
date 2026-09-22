package tools

import (
	"context"
	"reflect"
	"testing"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func TestZeroContextNeedsNoLanguageServer(t *testing.T) {
	locations := []protocol.Location{
		{Range: protocol.Range{Start: protocol.Position{Line: 1}}},
		{Range: protocol.Range{Start: protocol.Position{Line: 1}}},
		{Range: protocol.Range{Start: protocol.Position{Line: 3}}},
		{Range: protocol.Range{Start: protocol.Position{Line: 5}}},
	}
	// A nil client deliberately catches any accidental container request.
	got, err := GetLineRangesToDisplay(context.Background(), nil, locations, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{1: true, 3: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	got, err = GetLineRangesToDisplay(context.Background(), nil, nil, 0, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty document: lines = %v, error = %v", got, err)
	}
}
