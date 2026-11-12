package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

type AuthKey struct{ ent.Schema }

func (AuthKey) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "auth_keys"}}
}

func (AuthKey) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").SchemaType(map[string]string{dialect.Postgres: "integer"}),
		field.String("key_id").MaxLen(255).Unique(),
		field.String("data").SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}
