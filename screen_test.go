package screen

import (
	"context"
	"fmt"
	"testing"
)

func TestNew(t *testing.T) {
	s, err := New(context.Background(), "banana")
	if err != nil {
		t.Error(err)
	}

	fmt.Println(s)
}

func TestStuff(t *testing.T) {
	s, err := Get("banana")
	if err != nil {
		t.Error(err)
	}
	s.Stuff("echo", "hello\n")
}

func TestHardcopy(t *testing.T) {
	s, err := Get("banana")
	if err != nil {
		t.Error(err)
	}
	text, err := s.HardcopyString()
	if err != nil {
		t.Error(err)
	}
	fmt.Println(text)
}

func TestStuffReturnGetOutput(t *testing.T) {
	s, err := Get("banana")
	if err != nil {
		t.Error(err)
	}
	t.Log(s.Process.Pid)
	str, err := s.StuffReturnGetOutput(context.Background(), "echo", "hello sir")
	if err != nil {
		t.Error(err)
	}
	t.Log(str)
}
