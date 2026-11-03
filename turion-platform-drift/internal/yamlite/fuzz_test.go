package yamlite

import "testing"

func FuzzParse(f *testing.F) {
	seeds := []string{
		"a: 1\nb:\n  - x\n  - y: 2\n",
		"apiVersion: v1\nkind: List\nitems: []\n",
		"a: [1, 2, {b: c}]\n",
		"---\na: 1\n---\nb: 2\n",
		"key: |\n  block\n  text\n",
		"key: \"quoted: value\"\n",
		"a: &anchor 1\nb: *anchor\n",
		"",
		"\x00\xff",
		"a: " + string(make([]byte, 64)),
		"a:\n\t- tab\n",
		"a: [unclosed\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		docs, err := Parse(src)
		if err != nil {
			return // errors are a valid outcome; panics are not
		}
		for _, d := range docs {
			_, _ = GetString(d.Value, "apiVersion")
			_, _ = GetString(d.Value, "metadata", "name")
			var flat []string
			// Flatten via the validator's walker contract: deep recursion on a
			// hostile document must not blow the stack.
			flattenForFuzz(d.Value, &flat)
		}
	})
}

func flattenForFuzz(node Node, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for _, val := range v {
			flattenForFuzz(val, out)
		}
	case []any:
		for _, val := range v {
			flattenForFuzz(val, out)
		}
	case string:
		*out = append(*out, v)
	}
}
