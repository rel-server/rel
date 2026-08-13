package rel

import "sales-way.com/server/pg"

type ParsingContext struct {
	Tables         pg.DBAllTables
	Functions      pg.DBFunctionMap
	RelationshipNb int
}
