; Tree-sitter capture queries for TypeScript / TSX.
;
; Captures are consumed by internal/source_live/parser_ts.go (which
; currently walks the AST manually for speed and tolerance to malformed
; trees, but mirrors these patterns 1:1). When the resolver migrates to
; query-driven extraction (planned for a follow-up), this file is the
; single source of truth — keep parser_ts.go in lock-step.
;
; Capture semantics:
;   @function   — top-level / namespace-level named function declaration
;   @method     — class method (instance or static)
;   @arrow      — arrow / function expression bound to a const/let/field
;   @class      — class declaration (TypeDecl in code.core)
;   @interface  — interface declaration (TypeDecl in code.core)
;   @import     — module import; emits a Reference edge in code.core
;   @ref        — bare identifier reference (call site, type position)
;
; Field captures use @name.<thing> for the symbol's local identifier and
; @sig.<thing>.{params,return} for signature reconstruction.

;; Top-level functions.
(function_declaration
  name: (identifier) @name.function
  parameters: (formal_parameters) @sig.function.params
  return_type: (type_annotation)? @sig.function.return) @function

(generator_function_declaration
  name: (identifier) @name.function
  parameters: (formal_parameters) @sig.function.params) @function

;; Class methods (including get/set/static and constructors).
(method_definition
  name: (_) @name.method
  parameters: (formal_parameters) @sig.method.params
  return_type: (type_annotation)? @sig.method.return) @method

(abstract_method_signature
  name: (_) @name.method
  parameters: (formal_parameters) @sig.method.params) @method

;; Arrow functions assigned to a binding — `const foo = () => ...`.
(variable_declarator
  name: (identifier) @name.arrow
  value: (arrow_function
           parameters: (formal_parameters) @sig.arrow.params
           return_type: (type_annotation)? @sig.arrow.return) @arrow)

(variable_declarator
  name: (identifier) @name.arrow
  value: (function_expression
           parameters: (formal_parameters) @sig.arrow.params) @arrow)

;; Arrow / function-expression class fields.
(public_field_definition
  name: (_) @name.arrow
  value: (arrow_function
           parameters: (formal_parameters) @sig.arrow.params) @arrow)

;; Type declarations.
(class_declaration
  name: (type_identifier) @name.class) @class

(abstract_class_declaration
  name: (type_identifier) @name.class) @class

(interface_declaration
  name: (type_identifier) @name.interface) @interface

;; Imports — feed code.core Reference edges and the path_glob anchor.
(import_statement
  source: (string) @import.source) @import

;; Call sites — @ref captures feed call_neighborhood anchor evaluation.
(call_expression
  function: (identifier) @ref) @ref.call

(call_expression
  function: (member_expression
              property: (property_identifier) @ref)) @ref.method
