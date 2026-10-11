package config

import (
	"strings"
	"testing"
)

func TestGenI1AllProfiles(t *testing.T) {
	for _, profile := range I1Profiles() {
		chain, label, err := GenI1(profile, "example.com")
		if err != nil {
			t.Errorf("GenI1(%s): %v", profile, err)
			continue
		}
		if chain == "" {
			t.Errorf("GenI1(%s): пустая цепочка", profile)
		}
		if !strings.Contains(label, profile) {
			t.Errorf("GenI1(%s): label %q не содержит профиль", profile, label)
		}
	}
}

func TestGenI1Random(t *testing.T) {
	chain, label, err := GenI1(I1ProfileRandom, "")
	if err != nil {
		t.Fatalf("GenI1(random): %v", err)
	}
	if chain == "" || label == "" {
		t.Fatalf("GenI1(random) вернул пустое: chain=%q label=%q", chain, label)
	}
}

func TestGenI1UnknownProfile(t *testing.T) {
	if _, _, err := GenI1("does-not-exist", ""); err == nil {
		t.Error("ожидалась ошибка для неизвестного профиля")
	}
}

func TestDefaultI1IsChain(t *testing.T) {
	i1 := DefaultI1()
	if i1 == "" {
		t.Fatal("DefaultI1 пуст")
	}
	if !strings.Contains(i1, "<") {
		t.Errorf("DefaultI1 %q не похож на цепочку директив", i1)
	}
}

func TestIsI1Profile(t *testing.T) {
	for _, p := range append(I1Profiles(), I1ProfileRandom) {
		if !IsI1Profile(p) {
			t.Errorf("IsI1Profile(%s) = false", p)
		}
	}
	if IsI1Profile("nope") {
		t.Error("IsI1Profile(nope) = true")
	}
}

func TestGenDNSI1BadHost(t *testing.T) {
	if _, err := genDNSI1(""); err == nil {
		t.Error("ожидалась ошибка для пустого host")
	}
}
