package hcl2shim

import (
	"fmt"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestFlatmapValueFromHCL2(t *testing.T) {
	/*tests := []struct {
		Value cty.Value
		Want  map[string]string
	}{}*/
}

func TestHCL2ValueFromFlatmap(t *testing.T) {
	tests := []struct {
		Flatmap map[string]string
		Type    cty.Type
		Want    cty.Value
		WantErr string
	}{
		{
			Flatmap: map[string]string{},
			Type:    cty.EmptyObject,
			Want:    cty.EmptyObjectVal,
		},
		{
			Flatmap: map[string]string{
				"ignored": "foo",
			},
			Type: cty.EmptyObject,
			Want: cty.EmptyObjectVal,
		},
		{
			Flatmap: map[string]string{
				"foo": "blah",
				"bar": "true",
				"baz": "12.5",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.String,
				"bar": cty.Bool,
				"baz": cty.Number,
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.StringVal("blah"),
				"bar": cty.True,
				"baz": cty.NumberFloatVal(12.5),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.#": "0",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.List(cty.String),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.ListValEmpty(cty.String),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.#": "1",
				"foo.0": "hello",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.List(cty.String),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.ListVal([]cty.Value{
					cty.StringVal("hello"),
				}),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.#": "2",
				"foo.0": "true",
				"foo.1": "false",
				"foo.2": "ignored", // (because the count is 2, so this is out of range)
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.List(cty.Bool),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.ListVal([]cty.Value{
					cty.True,
					cty.False,
				}),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.#": "1",
				"foo.0": "hello",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.Tuple([]cty.Type{
					cty.String,
					cty.Bool,
				}),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.TupleVal([]cty.Value{
					cty.StringVal("hello"),
					cty.NullVal(cty.Bool),
				}),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.#": "0",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.Set(cty.String),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.SetValEmpty(cty.String),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.#":        "1",
				"foo.24534534": "hello",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.Set(cty.String),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.SetVal([]cty.Value{
					cty.StringVal("hello"),
				}),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.#":        "1",
				"foo.24534534": "true",
				"foo.95645644": "true",
				"foo.34533452": "false",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.Set(cty.Bool),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.SetVal([]cty.Value{
					cty.True,
					cty.False,
				}),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.%": "0",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.Map(cty.String),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.MapValEmpty(cty.String),
			}),
		},
		{
			Flatmap: map[string]string{
				"foo.%":       "0",
				"foo.baz":     "true",
				"foo.bar.baz": "false",
			},
			Type: cty.Object(map[string]cty.Type{
				"foo": cty.Map(cty.Bool),
			}),
			Want: cty.ObjectVal(map[string]cty.Value{
				"foo": cty.MapVal(map[string]cty.Value{
					"baz":     cty.StringVal("true"),
					"bar.baz": cty.StringVal("false"),
				}),
			}),
		},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%#v as %#v", test.Flatmap, test.Type), func(t *testing.T) {
			got, err := HCL2ValueFromFlatmap(test.Flatmap, test.Type)

			if test.WantErr != "" {
				if err == nil {
					t.Fatalf("succeeded; want error: %s", test.WantErr)
				}
				if got, want := err.Error(), test.WantErr; got != want {
					t.Fatalf("wrong error\ngot:  %s\nwant: %s", got, want)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %s", err.Error())
				}
			}

			if !got.RawEquals(test.Want) {
				t.Errorf("wrong result\ngot:  %#v\nwant: %#v", got, test.Want)
			}
		})
	}
}
