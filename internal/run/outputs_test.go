package run

import "testing"

func TestParseOutputsJSONAndText(t *testing.T) {
	res := ParseOutputs("{\"status\":\"pass\",\"summary\":\"ok\",\"findings\":[\"a\"]}", "", 0, "fail")
	if res.Status != "pass" || res.Summary != "ok" || len(res.Findings) != 1 {
		t.Fatalf("%+v", res)
	}
	res = ParseOutputs("not json", "boom", 1, "fail")
	if res.Status != "fail" || res.Summary != "not json" || res.Findings[0] != "boom" {
		t.Fatalf("%+v", res)
	}
}
