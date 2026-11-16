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

// FillSearchPath fills infos.SearchPath with the connecting role's resolved search path, in lookup order ;
// never a switched-to role's.
func FillSearchPath(infos *DbInfos, conn *pgx.Conn) error {
	row := conn.QueryRow(context.Background(), INFO_QUERY_SEARCH_PATH)
	if err := row.Scan(&infos.SearchPath); err != nil {
		return oops.With("query", INFO_QUERY_SEARCH_PATH).Wrapf(err, "failed to introspect search path")
	}
	return nil
}

// current_schemas(true) includes the implicit pg_catalog.
var INFO_QUERY_SEARCH_PATH = /* sql */ `SELECT current_schemas(true) AS search_path`
