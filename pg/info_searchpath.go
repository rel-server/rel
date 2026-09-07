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

	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"
)

// FillSearchPath introspects the connecting role's resolved search path
// into infos.SearchPath, in lookup order. Live-introspected, not static
// config : query-engine.md ## Search path only ever respects the base
// connection's actual search path, never a switched-to role's own.
func FillSearchPath(infos *DbInfos, conn *pgx.Conn) error {
	row := conn.QueryRow(context.Background(), INFO_QUERY_SEARCH_PATH)
	if err := row.Scan(&infos.SearchPath); err != nil {
		return oops.With("query", INFO_QUERY_SEARCH_PATH).Wrapf(err, "failed to introspect search path")
	}
	return nil
}

// current_schemas(true) : the already-$user-expanded, existence-filtered
// search path, including the implicit trailing pg_catalog — exactly the
// order an unqualified-name lookup needs, without hand-parsing the raw
// search_path GUC string or separately filtering nonexistent schemas.
var INFO_QUERY_SEARCH_PATH = /* sql */ `SELECT current_schemas(true) AS search_path`
