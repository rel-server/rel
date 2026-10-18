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

package scenarios

import "github.com/rel-server/rel/benchmarks/harness"

// All returns every scenario, reads first.
func All() []harness.Scenario {
	return withCBOR(append(reads(), writes()...))
}

// withCBOR gives every scenario a rel-cbor implementation : rel's own request, converted to CBOR by the client, and
// its answer converted back to JSON before it is compared with the other servers'.
func withCBOR(list []harness.Scenario) []harness.Scenario {
	for _, sc := range list {
		impl, ok := sc.Impls[harness.Rel]
		if !ok || impl.Support == harness.Unsupported {
			continue
		}
		if norm := impl.Normalize; norm != nil {
			impl.Normalize = func(body []byte) (any, error) {
				js, err := harness.CBORToJSON(body)
				if err != nil {
					return nil, err
				}
				return norm(js)
			}
		}
		sc.Impls[harness.RelCBOR] = impl
	}
	return list
}
