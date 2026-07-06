package report

import "testing"

func TestMongoSignature_wireJSON(t *testing.T) {
	sig := MongoSignature([]byte(`{"insert":"orders","documents":[]}`), MongoHints{})
	if sig != "mongodb:insert:orders" {
		t.Fatalf("got %q", sig)
	}
}

func TestMongoSignature_extraStringField(t *testing.T) {
	sig := MongoSignature([]byte(`{"insert":"orders","audit":"n1"}`), MongoHints{})
	if sig != "mongodb:insert:orders" {
		t.Fatalf("got %q", sig)
	}
}

func TestMongoSignature_spanHints(t *testing.T) {
	sig := MongoSignature([]byte(`{}`), MongoHints{Operation: "insert", Collection: "orders"})
	if sig != "mongodb:insert:orders" {
		t.Fatalf("got %q", sig)
	}
}

func TestMongoSignature_insertManyNormalized(t *testing.T) {
	sig := MongoSignature([]byte(`{}`), MongoHints{Operation: "insertMany", Collection: "orders"})
	if sig != "mongodb:insert:orders" {
		t.Fatalf("got %q", sig)
	}
}

func TestMongoSignature_wrappedQuery(t *testing.T) {
	sig := MongoSignature([]byte(`{"query":"insert orders"}`), MongoHints{})
	if sig != "mongodb:insert:orders" {
		t.Fatalf("got %q", sig)
	}
}

func TestMongoSignature_hintsOverrideWrappedQuery(t *testing.T) {
	sig := MongoSignature(
		[]byte(`{"query":"garbled"}`),
		MongoHints{Operation: "find", Collection: "items"},
	)
	if sig != "mongodb:find:items" {
		t.Fatalf("got %q", sig)
	}
}

func TestMongoSignature_pythonPymongo(t *testing.T) {
	sig := MongoSignature([]byte(`{"query":"insert"}`), MongoHints{Collection: "orders"})
	if sig != "mongodb:insert:orders" {
		t.Fatalf("got %q", sig)
	}
}

func TestMongoOperationFromStatement(t *testing.T) {
	if got := MongoOperationFromStatement("insert"); got != "insert" {
		t.Fatalf("got %q", got)
	}
	if got := MongoOperationFromStatement(`insert {'order_id': '1'}`); got != "insert" {
		t.Fatalf("got %q", got)
	}
}

func TestMongoSignature_fallbackHash(t *testing.T) {
	sig := MongoSignature([]byte(`{"documents":[]}`), MongoHints{})
	if sig == "" || sig[:15] != "mongodb:unknown" {
		t.Fatalf("got %q", sig)
	}
}

func TestEgressSignature_mongodbDelegates(t *testing.T) {
	sig := EgressSignature("mongodb", []byte(`{"find":"users","filter":{}}`))
	if sig != "mongodb:find:users" {
		t.Fatalf("got %q", sig)
	}
}
