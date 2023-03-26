// Copyright 2023 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package main tests precedence of main paths. Building this package will pass the acceptance test.
package main

import (
	"fmt"
	"net/http"
	"os"

	"rsc.io/quote"
)

func handler(w http.ResponseWriter, r *http.Request) {
	if quote.Hello() == "Hello, world." {
		fmt.Fprintf(w, "PASS")
	} else {
		fmt.Fprintln(w, "FAIL")
	}
}

func main() {
	http.HandleFunc("/", handler)
	http.ListenAndServe(":"+os.Getenv("PORT"), nil)
}
