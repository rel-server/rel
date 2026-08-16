// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pg

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"
)

func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, "\"", "\"\"")
}

// Scan the result of a json_agg query into a target, because the json deserialization is actually easier to use that defining custom types with pgx, and since we only do it once to refresh the schema information, we don't bother.
func scanIntoThroughJsonAgg(conn *pgx.Conn, query string, target any) error {
	oc := oops.With("query", query)

	rows, err := conn.Query(context.Background(), query)
	if err != nil {
		return oc.Wrapf(err, "failed to query")
	}
	defer rows.Close()

	// json_agg over zero rows is SQL NULL, not '[]' — scan into a *string so an
	// empty result set doesn't error, and leave target at its zero value.
	var jsonstr *string

	if !rows.Next() {
		return oc.Errorf("no rows found")
	}

	err = rows.Scan(&jsonstr)
	if err != nil {
		return oc.Wrapf(err, "failed to scan json")
	}

	if jsonstr == nil {
		return nil
	}

	err = json.Unmarshal([]byte(*jsonstr), &target)
	if err != nil {
		return oc.Wrapf(err, "failed to unmarshal")
	}
	return nil
}
