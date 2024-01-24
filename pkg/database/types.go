// Copyright 2023 Paolo Fabio Zaino
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

package database

// Source represents the structure of the Sources table
// for a record we have decided we need to crawl.
// Id represents the unique identifier of the source.
// URL represents the URL of the source.
// Restricted indicates whether the crawling has to be restricted to the source domain or not.
// Flags represents additional flags associated with the source.
type Source struct {
	ID         int
	URL        string
	Restricted bool
	Flags      int
}
