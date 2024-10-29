// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testar

import (
	"fmt"
	"strconv"
	"strings"
)

func ExampleRead() {
	data := []byte(`
-- foo --
hello

-- bar --
world
`)

	var into struct {
		Foo string `testar:"foo"`
		Bar []byte `testar:"bar"`
	}

	_ = Read(data, &into)
	fmt.Printf("foo: %T(%q)\n", into.Foo, into.Foo)
	fmt.Printf("bar: %T(%q)\n", into.Bar, into.Bar)

	// Output:
	// foo: string("hello\n\n")
	// bar: []uint8("world\n")
}

func ExampleParser() {
	data := []byte(`
-- foobar --
377927
`)

	var into struct {
		Foobar int `testar:"foobar,atoi"`
	}

	_ = Read(data, &into, Parser("atoi", func(file []byte, into any) error {
		n, err := strconv.Atoi(strings.TrimSpace(string(file)))
		if err != nil {
			return err
		}
		*(into.(*int)) = n
		return nil
	}))

	fmt.Printf("foobar: %T(%d)\n", into.Foobar, into.Foobar)

	// Output:
	// foobar: int(377927)
}
