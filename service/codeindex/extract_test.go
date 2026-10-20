package codeindex

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func getSymbol(f Facts, name, parent string) *Symbol {
	for i := range f.Symbols {
		if f.Symbols[i].Name == name && f.Symbols[i].Parent == parent {
			return &f.Symbols[i]
		}
	}
	return nil
}

func hasEdge(f Facts, from, to, kind string) bool {
	for _, e := range f.Edges {
		if e.FromSymbol == from && e.ToRaw == to && e.Kind == kind {
			return true
		}
	}
	return false
}

func hasImport(f Facts, target string) bool {
	for _, e := range f.Edges {
		if e.FromSymbol == "" && e.Kind == EdgeImport && e.ToRaw == target {
			return true
		}
	}
	return false
}

func TestLanguageFor(t *testing.T) {
	cases := map[string]string{
		"a/b/main.go":      langGo,
		"x.py":             langPython,
		"types.pyi":        langPython,
		"app.tsx":          langTypeScript,
		"lib.mjs":          langJavaScript,
		"Main.java":        langJava,
		"Foo.kt":           langKotlin,
		"build.gradle.kts": langKotlin,
		"A.scala":          langScala,
		"Program.cs":       langCSharp,
		"a.c":              langC,
		"a.h":              langC,
		"a.cpp":            langCPP,
		"a.hpp":            langCPP,
		"lib.rs":           langRust,
		"App.swift":        langSwift,
		"main.dart":        langDart,
		"index.php":        langPHP,
		"task.rb":          langRuby,
		"init.lua":         langLua,
	}
	for path, want := range cases {
		got, ok := LanguageFor(path)
		if !ok || got != want {
			t.Errorf("LanguageFor(%q) = (%q,%v), want (%q,true)", path, got, ok, want)
		}
	}
	for _, path := range []string{"README.md", "Makefile", "a.txt", "noext"} {
		if got, ok := LanguageFor(path); ok {
			t.Errorf("LanguageFor(%q) = (%q,true), want not ok", path, got)
		}
	}
}

func TestExtractGo(t *testing.T) {
	src := `package demo

import (
	"fmt"
	util "example.com/pkg/util"
)

const Answer = 42

var global string

type Base struct{}

type Reader interface {
	Read(p []byte) (int, error)
}

type Service struct {
	Base
	*util.Client
	Name string
}

type Handler interface {
	Reader
	Handle() error
}

type ID int

func NewService(name string) *Service {
	local := 1
	_ = local
	return &Service{Name: name}
}

func (s *Service) Run() error {
	return nil
}
`

	f, ok := Extract("svc.go", []byte(src))
	if !ok {
		t.Fatalf("Extract returned ok=false")
	}
	if f.Language != langGo {
		t.Fatalf("language = %q", f.Language)
	}

	want := []struct {
		name, kind, parent string
	}{
		{"Base", KindStruct, ""},
		{"Reader", KindInterface, ""},
		{"Service", KindStruct, ""},
		{"Handler", KindInterface, ""},
		{"ID", KindType, ""},
		{"Answer", KindConst, ""},
		{"global", KindVar, ""},
		{"NewService", KindFunc, ""},
		{"Run", KindMethod, "Service"},
	}
	for _, w := range want {
		s := getSymbol(f, w.name, w.parent)
		if s == nil {
			t.Fatalf("missing symbol %s (parent %q)", w.name, w.parent)
		}
		if s.Kind != w.kind {
			t.Errorf("%s kind = %q, want %q", w.name, s.Kind, w.kind)
		}
	}
	if s := getSymbol(f, "local", ""); s != nil {
		t.Errorf("local variable must not be extracted: %+v", s)
	}

	if s := getSymbol(f, "NewService", ""); !strings.Contains(s.Signature, "name string") ||
		!strings.Contains(s.Signature, "*Service") {
		t.Errorf("NewService signature = %q", s.Signature)
	}

	run := getSymbol(f, "Run", "Service")
	if run.LineStart <= 0 || run.LineEnd <= run.LineStart {
		t.Errorf("Run lines = %d..%d", run.LineStart, run.LineEnd)
	}

	for _, imp := range []string{"fmt", "example.com/pkg/util"} {
		if !hasImport(f, imp) {
			t.Errorf("missing import edge %q: %+v", imp, f.Edges)
		}
	}
	if !hasEdge(f, "Service", "Base", EdgeEmbeds) {
		t.Errorf("missing embeds Service->Base: %+v", f.Edges)
	}
	if !hasEdge(f, "Service", "util.Client", EdgeEmbeds) {
		t.Errorf("missing embeds Service->util.Client: %+v", f.Edges)
	}
	if !hasEdge(f, "Handler", "Reader", EdgeEmbeds) {
		t.Errorf("missing embeds Handler->Reader: %+v", f.Edges)
	}
}

func TestExtractPython(t *testing.T) {
	src := `import os
from pkg.sub import thing

CONST_VALUE = 1

class Base:
    def ping(self):
        return 1

class Service(Base):
    def __init__(self, name):
        self.name = name

    def run(self, ctx):
        if ctx:
            return 2
        return 3

@mydecorator
def helper(x: int) -> str:
    return str(x)
`

	f, ok := Extract("svc.py", []byte(src))
	if !ok {
		t.Fatalf("Extract returned ok=false")
	}
	if s := getSymbol(f, "Service", ""); s == nil || s.Kind != KindClass {
		t.Fatalf("missing class Service: %+v", f.Symbols)
	}
	if s := getSymbol(f, "ping", "Base"); s == nil || s.Kind != KindMethod {
		t.Errorf("missing method Base.ping: %+v", f.Symbols)
	}
	run := getSymbol(f, "run", "Service")
	if run == nil || run.Kind != KindMethod {
		t.Fatalf("missing method Service.run: %+v", f.Symbols)
	}
	if run.LineStart != 14 || run.LineEnd != 17 {
		t.Errorf("run lines = %d..%d, want 14..17", run.LineStart, run.LineEnd)
	}
	if s := getSymbol(f, "helper", ""); s == nil || !strings.HasPrefix(s.Signature, "@mydecorator def helper") {
		t.Errorf("helper symbol = %+v", s)
	}
	if s := getSymbol(f, "CONST_VALUE", ""); s == nil || s.Kind != KindConst {
		t.Errorf("CONST_VALUE = %+v", s)
	}
	if !hasEdge(f, "Service", "Base", EdgeExtends) {
		t.Errorf("missing extends Service->Base: %+v", f.Edges)
	}
	if !hasImport(f, "os") || !hasImport(f, "pkg.sub") {
		t.Errorf("missing python imports: %+v", f.Edges)
	}
}

func TestExtractTypeScript(t *testing.T) {
	src := `import { Component } from "@angular/core";
import type { Foo } from "./foo";

export interface Shape extends Base {
  area(): number;
}

export class Circle extends Base implements Shape {
  radius: number = 0;

  constructor(r: number) {
    this.radius = r;
  }

  area(): number {
    return this.radius * 3;
  }
}

export function makeCircle(r: number): Circle {
  return new Circle(r);
}
`

	f, ok := Extract("circle.ts", []byte(src))
	if !ok {
		t.Fatalf("Extract returned ok=false")
	}
	if s := getSymbol(f, "Shape", ""); s == nil || s.Kind != KindInterface {
		t.Fatalf("missing interface Shape: %+v", f.Symbols)
	}
	if s := getSymbol(f, "Circle", ""); s == nil || s.Kind != KindClass {
		t.Fatalf("missing class Circle: %+v", f.Symbols)
	}
	if s := getSymbol(f, "radius", "Circle"); s == nil {
		t.Errorf("missing property radius: %+v", f.Symbols)
	}
	ctor := getSymbol(f, "constructor", "Circle")
	if ctor == nil || ctor.LineEnd != 13 {
		t.Errorf("constructor = %+v", ctor)
	}
	area := getSymbol(f, "area", "Circle")
	if area == nil || area.LineEnd != 17 {
		t.Errorf("area = %+v", area)
	}
	if s := getSymbol(f, "makeCircle", ""); s == nil || s.Kind != KindFunc {
		t.Errorf("missing function makeCircle: %+v", f.Symbols)
	}
	if !hasEdge(f, "Circle", "Base", EdgeExtends) {
		t.Errorf("missing extends Circle->Base: %+v", f.Edges)
	}
	if !hasEdge(f, "Circle", "Shape", EdgeImplements) {
		t.Errorf("missing implements Circle->Shape: %+v", f.Edges)
	}
	if !hasEdge(f, "Shape", "Base", EdgeExtends) {
		t.Errorf("missing extends Shape->Base: %+v", f.Edges)
	}
	if !hasImport(f, "@angular/core") || !hasImport(f, "./foo") {
		t.Errorf("missing ts imports: %+v", f.Edges)
	}
}

func TestExtractJava(t *testing.T) {
	src := `package demo;

import java.util.List;

public class Service extends Base implements Runnable, AutoCloseable {
    private int count;

    public Service(int count) {
        this.count = count;
    }

    @Override
    public void run() {
        System.out.println("run");
    }
}
`

	f, ok := Extract("Service.java", []byte(src))
	if !ok {
		t.Fatalf("Extract returned ok=false")
	}
	if s := getSymbol(f, "Service", ""); s == nil || s.Kind != KindClass {
		t.Fatalf("missing class Service: %+v", f.Symbols)
	}
	if s := getSymbol(f, "Service", "Service"); s == nil || s.Kind != KindMethod {
		t.Errorf("missing constructor: %+v", f.Symbols)
	}
	run := getSymbol(f, "run", "Service")
	if run == nil || run.LineEnd != 15 {
		t.Errorf("run = %+v", run)
	}
	if !hasEdge(f, "Service", "Base", EdgeExtends) {
		t.Errorf("missing extends: %+v", f.Edges)
	}
	if !hasEdge(f, "Service", "Runnable", EdgeImplements) || !hasEdge(f, "Service", "AutoCloseable", EdgeImplements) {
		t.Errorf("missing implements: %+v", f.Edges)
	}
	if !hasImport(f, "java.util.List") {
		t.Errorf("missing java import: %+v", f.Edges)
	}
}

func TestExtractCpp(t *testing.T) {
	src := `#include <vector>
#include "util.h"

namespace demo {

class Base {
public:
    virtual void run() = 0;
};

class Service : public Base {
public:
    void run() override {
        for (int i = 0; i < 10; i++) {
        }
    }
};

int helper(int x) {
    return x;
}
}
`

	f, ok := Extract("svc.cpp", []byte(src))
	if !ok {
		t.Fatalf("Extract returned ok=false")
	}
	if s := getSymbol(f, "Base", ""); s == nil || s.Kind != KindClass {
		t.Fatalf("missing class Base: %+v", f.Symbols)
	}
	if s := getSymbol(f, "Service", ""); s == nil || s.Kind != KindClass {
		t.Fatalf("missing class Service: %+v", f.Symbols)
	}
	if s := getSymbol(f, "run", "Service"); s == nil || s.Kind != KindMethod {
		t.Errorf("missing method Service.run: %+v", f.Symbols)
	}
	if s := getSymbol(f, "helper", ""); s == nil || s.Kind != KindFunc {
		t.Errorf("missing function helper: %+v", f.Symbols)
	}
	if !hasImport(f, "vector") || !hasImport(f, "util.h") {
		t.Errorf("missing include edges: %+v", f.Edges)
	}
	if !hasEdge(f, "Service", "Base", EdgeExtends) {
		t.Errorf("missing cpp inheritance: %+v", f.Edges)
	}
}

func TestExtractSkipsCommentsAndStrings(t *testing.T) {
	ts := `// function fakeComment() {}
/* class FakeBlock { } */
const s = "function fakeString() { }";
const t = 'class NotAClass {}';
export function real(ctx: string) {
  return ctx;
}
`
	f, ok := Extract("skip.ts", []byte(ts))
	if !ok {
		t.Fatalf("Extract returned ok=false")
	}
	for _, name := range []string{"fakeComment", "fakeString", "FakeBlock", "NotAClass"} {
		if s := getSymbol(f, name, ""); s != nil {
			t.Errorf("comment/string declaration leaked: %+v", s)
		}
	}
	if getSymbol(f, "real", "") == nil {
		t.Errorf("real function missing: %+v", f.Symbols)
	}

	py := `# def fake(): pass
doc = "def fakeString(): pass"

def real(x):
    return x
`
	pf, _ := Extract("skip.py", []byte(py))
	for _, name := range []string{"fake", "fakeString"} {
		if s := getSymbol(pf, name, ""); s != nil {
			t.Errorf("python comment/string leaked: %+v", s)
		}
	}
	if getSymbol(pf, "real", "") == nil {
		t.Errorf("python real function missing: %+v", pf.Symbols)
	}
}

func TestExtractMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		path string
		src  []byte
	}{
		{"empty-go", "a.go", nil},
		{"truncated-go", "a.go", []byte("package x\nfunc f( {")},
		{"truncated-ts", "a.ts", []byte("export class A extends {")},
		{"unclosed-c", "a.c", []byte("int main() {\n  if (x) {")},
		{"unterminated-string-py", "a.py", []byte("def f(:\n  return \"abc")},
		{"binary", "a.py", []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x0A}},
		{"binary-ts", "a.ts", []byte{0x00, 0x7B, 0x7D, 0xFF}},
		{"nul", "a.ts", []byte("class A {\x00}\x00")},
		{"huge-line", "a.rb", []byte(strings.Repeat("def x\n", 10) + strings.Repeat("a", 100000))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _ = Extract(c.path, c.src)
		})
	}
}

func TestExtractTooLarge(t *testing.T) {
	src := []byte(strings.Repeat("// pad\n", maxSourceLines+1))
	if _, ok := Extract("big.ts", src); ok {
		t.Fatalf("expected ok=false for oversized file")
	}
	big := make([]byte, maxSourceBytes+1)
	for i := range big {
		big[i] = ' '
	}
	if _, ok := Extract("big.ts", big); ok {
		t.Fatalf("expected ok=false for oversized bytes")
	}
}

func TestSignatureTruncation(t *testing.T) {
	long := strings.Repeat("x", 300)
	src := "package p\n\nfunc foo(" + long + " int) error { return nil }\n"
	f, ok := Extract("p.go", []byte(src))
	if !ok {
		t.Fatalf("Extract returned ok=false")
	}
	s := getSymbol(f, "foo", "")
	if s == nil {
		t.Fatalf("missing func foo")
	}
	if !strings.HasSuffix(s.Signature, "...") {
		t.Errorf("signature not truncated: %q", s.Signature)
	}
	if n := utf8.RuneCountInString(s.Signature); n != maxSignatureRunes+3 {
		t.Errorf("signature rune length = %d, want %d", n, maxSignatureRunes+3)
	}
	if strings.Contains(s.Signature, "\n") {
		t.Errorf("signature contains newline: %q", s.Signature)
	}
}

func mustSymbol(t *testing.T, f Facts, name, kind, parent string) {
	t.Helper()
	s := getSymbol(f, name, parent)
	if s == nil {
		t.Fatalf("missing symbol %q (parent %q): %+v", name, parent, f.Symbols)
	}
	if kind != "" && s.Kind != kind {
		t.Errorf("%s kind = %q, want %q", name, s.Kind, kind)
	}
}

func mustEdge(t *testing.T, f Facts, from, to, kind string) {
	t.Helper()
	if !hasEdge(f, from, to, kind) {
		t.Errorf("missing edge %s -%s-> %s: %+v", from, kind, to, f.Edges)
	}
}

func TestExtractOtherLanguages(t *testing.T) {
	t.Run("rust", func(t *testing.T) {
		src := `use std::fmt::{self, Display};

pub struct Point {
    pub x: i32,
}

impl Display for Point {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(f, "{}", self.x)
    }
}

pub fn origin() -> Point {
    Point { x: 0 }
}
`
		f, ok := Extract("point.rs", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Point", KindStruct, "")
		mustSymbol(t, f, "origin", KindFunc, "")
		mustSymbol(t, f, "fmt", KindMethod, "Point")
		mustEdge(t, f, "Point", "Display", EdgeImplements)
		if !hasImport(f, "std::fmt") {
			t.Errorf("missing rust use: %+v", f.Edges)
		}
	})

	t.Run("kotlin", func(t *testing.T) {
		src := `package demo

import kotlin.math.max

interface Shape {
    fun area(): Double
}

open class Circle(val r: Double) : Shape {
    override fun area(): Double = 3.14 * r
    fun describe(): String {
        return "circle"
    }
}
`
		f, ok := Extract("Circle.kt", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Shape", KindInterface, "")
		mustSymbol(t, f, "Circle", KindClass, "")
		mustSymbol(t, f, "area", KindMethod, "Circle")
		mustSymbol(t, f, "describe", KindMethod, "Circle")
		mustEdge(t, f, "Circle", "Shape", EdgeExtends)
	})

	t.Run("swift", func(t *testing.T) {
		src := `import Foundation

protocol Greeter {
    func greet() -> String
}

class Person: Greeter {
    let name: String = ""
    func greet() -> String {
        return "hi"
    }
}
`
		f, ok := Extract("Person.swift", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Greeter", KindInterface, "")
		mustSymbol(t, f, "Person", KindClass, "")
		mustSymbol(t, f, "greet", KindMethod, "Person")
		mustEdge(t, f, "Person", "Greeter", EdgeExtends)
	})

	t.Run("csharp", func(t *testing.T) {
		src := `using System;

namespace Demo {
    public class Base {
        public virtual void Run() { }
    }

    public class Service : Base, IDisposable {
        public void Run(int times) {
            for (int i = 0; i < times; i++) { }
        }
        public int Count { get; set; }
    }
}
`
		f, ok := Extract("Service.cs", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Base", KindClass, "")
		mustSymbol(t, f, "Service", KindClass, "")
		mustSymbol(t, f, "Run", KindMethod, "Service")
		mustSymbol(t, f, "Count", KindVar, "Service")
		mustEdge(t, f, "Service", "Base", EdgeExtends)
	})

	t.Run("php", func(t *testing.T) {
		src := `<?php
namespace App;

use App\Contracts\Shape;

abstract class Base implements Shape {
    public function area(): float {
        return 0.0;
    }
}

class Circle extends Base {
    public function area(): float {
        return 3.14;
    }
}
`
		f, ok := Extract("Circle.php", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Base", KindClass, "")
		mustSymbol(t, f, "Circle", KindClass, "")
		mustSymbol(t, f, "area", KindMethod, "Circle")
		mustEdge(t, f, "Base", "Shape", EdgeImplements)
		mustEdge(t, f, "Circle", "Base", EdgeExtends)
		if !hasImport(f, "App\\Contracts\\Shape") {
			t.Errorf("missing php use: %+v", f.Edges)
		}
	})

	t.Run("dart", func(t *testing.T) {
		src := `import 'dart:async';

abstract class Shape {
  double area();
}

class Circle extends Shape {
  final double r;
  Circle(this.r);
  @override
  double area() {
    return 3.14 * r;
  }
}
`
		f, ok := Extract("circle.dart", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Shape", KindClass, "")
		mustSymbol(t, f, "Circle", KindClass, "")
		mustSymbol(t, f, "area", KindMethod, "Circle")
		mustEdge(t, f, "Circle", "Shape", EdgeExtends)
	})

	t.Run("scala", func(t *testing.T) {
		src := `package demo

import scala.collection.mutable

trait Shape {
  def area(): Double
}

class Circle(r: Double) extends Shape {
  def area(): Double = 3.14 * r
  def describe(): String = "circle"
}
`
		f, ok := Extract("Circle.scala", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Shape", KindTrait, "")
		mustSymbol(t, f, "Circle", KindClass, "")
		mustSymbol(t, f, "area", KindMethod, "Circle")
		mustSymbol(t, f, "describe", KindMethod, "Circle")
		mustEdge(t, f, "Circle", "Shape", EdgeExtends)
	})

	t.Run("ruby", func(t *testing.T) {
		src := `require "json"

class Base
  def ping
    1
  end
end

class Service < Base
  def run(ctx)
    ctx
  end
end
`
		f, ok := Extract("service.rb", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "Base", KindClass, "")
		mustSymbol(t, f, "Service", KindClass, "")
		mustSymbol(t, f, "ping", KindMethod, "Base")
		mustSymbol(t, f, "run", KindMethod, "Service")
		mustEdge(t, f, "Service", "Base", EdgeExtends)
		if !hasImport(f, "json") {
			t.Errorf("missing ruby require: %+v", f.Edges)
		}
	})

	t.Run("lua", func(t *testing.T) {
		src := `local json = require("json")

local M = {}

function M.greet(name)
  return name
end

function helper(x)
  return x
end

return M
`
		f, ok := Extract("m.lua", []byte(src))
		if !ok {
			t.Fatalf("ok=false")
		}
		mustSymbol(t, f, "greet", KindMethod, "M")
		mustSymbol(t, f, "helper", KindFunc, "")
		if !hasImport(f, "json") {
			t.Errorf("missing lua require: %+v", f.Edges)
		}
	})
}

func TestExtractUnsupported(t *testing.T) {
	for _, path := range []string{"a.txt", "README.md", "Makefile"} {
		if f, ok := Extract(path, []byte("hello")); ok {
			t.Errorf("Extract(%q) ok=true, facts=%+v", path, f)
		}
	}
}
