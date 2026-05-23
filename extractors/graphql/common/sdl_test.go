package common

import "testing"

func TestParseSDL(t *testing.T) {
	src := `
type Query {
  user(id: ID!): User
  users: [User!]!
}

extend type Mutation {
  createUser(name: String!, age: Int): User!
}

type Subscription {
  userAdded: User
}
`
	got := ParseSDL(src)
	if len(got) != 4 {
		t.Fatalf("want 4 fields, got %d: %+v", len(got), got)
	}
	want := map[string]string{
		"user":        "query",
		"users":       "query",
		"createUser":  "mutation",
		"userAdded":   "subscription",
	}
	for _, f := range got {
		w, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected field %q", f.Name)
			continue
		}
		if f.OperationType != w {
			t.Errorf("field %q: op=%q, want %q", f.Name, f.OperationType, w)
		}
	}
	// Spot-check arguments + return type.
	for _, f := range got {
		switch f.Name {
		case "user":
			if len(f.Arguments) != 1 || f.Arguments[0] != "id" {
				t.Errorf("user.args = %v, want [id]", f.Arguments)
			}
			if f.ReturnType != "User" {
				t.Errorf("user.ret = %q", f.ReturnType)
			}
		case "createUser":
			if len(f.Arguments) != 2 {
				t.Errorf("createUser.args = %v", f.Arguments)
			}
			if f.ReturnType != "User!" {
				t.Errorf("createUser.ret = %q", f.ReturnType)
			}
		}
	}
}

func TestExtractGqlBlocks(t *testing.T) {
	src := "const schema = gql`\ntype Query { ping: String }\n`;\nconst other = graphql`type Mutation { do: Int }`;"
	blocks := ExtractGqlBlocks(src)
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d: %v", len(blocks), blocks)
	}
}
