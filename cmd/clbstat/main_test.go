package main

import "testing"

func TestDecodeRecordAcceptsObjectPayloadText(t *testing.T) {
	s := &stats{}
	rec, ok := s.decodeRecord([]byte(`{"payload":{"text":{"type":"response.create","model":"gpt-5.6"}}}`))
	if !ok {
		t.Fatal("object payload.text was rejected")
	}
	if _, ok := rec.ParseEvent(); !ok {
		t.Fatal("object payload.text did not reach frame.ParseEvent")
	}
}
