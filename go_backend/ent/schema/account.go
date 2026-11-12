package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

type Account struct{ ent.Schema }

func (Account) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "accounts"}}
}

func (Account) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").SchemaType(map[string]string{dialect.Postgres: "integer"}),
		field.String("access_token").SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("data").SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}
