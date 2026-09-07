package contract

import (
	"context"
	"reflect"
	"testing"
)

type searchRepositoryStub struct{}

func (searchRepositoryStub) GetActiveSearchContract(context.Context) (*ActiveSearchContract, error) {
	return nil, nil
}

func (searchRepositoryStub) CheckSearchReadiness(context.Context) (*SearchReadiness, error) {
	return nil, nil
}

func (searchRepositoryStub) SearchFullText(context.Context, FullTextSearchInput) ([]SearchHit, error) {
	return nil, nil
}

func (searchRepositoryStub) SearchExactVector(context.Context, ExactVectorSearchInput) ([]SearchHit, error) {
	return nil, nil
}

var _ SearchRepository = searchRepositoryStub{}

func TestSearchReadInputsDoNotCarryDerivedSpaceScope(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(FullTextSearchInput{}), reflect.TypeOf(ExactVectorSearchInput{})} {
		if _, ok := typ.FieldByName("SpaceID"); ok {
			t.Fatalf("%s exposes derived space scope", typ.Name())
		}
	}
	if input := (FullTextSearchInput{}); input.TeamID != "" || input.Query != "" || input.SourceKind != "" || input.Limit != 0 {
		t.Fatalf("unexpected zero value: %#v", input)
	}
	if input := (ExactVectorSearchInput{}); input.TeamID != "" || input.EmbeddingContractID != "" || input.SourceKind != "" || input.QueryEmbedding != nil || input.Limit != 0 {
		t.Fatalf("unexpected zero value: %#v", input)
	}
}
