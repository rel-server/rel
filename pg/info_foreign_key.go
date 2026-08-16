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

type IncomingForeignKey struct {
	Identifier       SqlIdentifier
	OtherRelation    *Relation
	OtherColumns     []*Column
	OtherColumnNames []string
	OtherIsUnique    bool // There is only one row that comes from that table that leads to me.

	// Targeted columns are necessarily unique.
	SelfColumnNames []string
	SelfColumns     []*Column
}

type OutgoingForeignKey struct {
	Identifier       SqlIdentifier
	OtherRelation    *Relation
	OtherColumns     []*Column
	OtherColumnNames []string

	SelfIsUnique    bool
	SelfColumnNames []string
	SelfColumns     []*Column
}
