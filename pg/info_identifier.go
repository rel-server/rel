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
	"fmt"
)

type SqlIdentifier struct {
	Schema string
	Name   string

	escapedName string
}

// EscapedString returns the full, escaped identifier ("schema"."name"), cached.
func (f *SqlIdentifier) EscapedString() string {
	if f.escapedName != "" {
		return f.escapedName
	}

	f.escapedName = fmt.Sprintf("%s.%s", EscapeIdentifierPart(f.Schema), EscapeIdentifierPart(f.Name))
	return f.escapedName
}

// EscapeIdentifierPart escapes a single SQL identifier part ; shared with dbauth.EscapeIdentifier.
func EscapeIdentifierPart(name string) string {
	return `"` + escapeQuotes(name) + `"`
}

// String returns the unescaped "schema.name", for map keys ; never put it in SQL.
func (f SqlIdentifier) String() string {
	return f.Schema + "." + f.Name
}
