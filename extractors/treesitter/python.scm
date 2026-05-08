; Tree-sitter capture queries for Python.
;
; Mirrors internal/source_live/parser_py.go. See typescript.scm for
; the capture-naming convention.
;
;   @function   — module-level function definition
;   @method     — class method (function_definition inside class_body)
;   @class      — class definition (TypeDecl in code.core)
;   @import     — `import` / `from … import …` (Reference edges)
;   @ref        — call sites and attribute references

;; Module-level functions.
(module
  (function_definition
    name: (identifier) @name.function
    parameters: (parameters) @sig.function.params
    return_type: (type)? @sig.function.return) @function)

;; Decorated module-level functions — recurse one level so the wrapper
;; doesn't hide the inner definition.
(module
  (decorated_definition
    definition: (function_definition
                  name: (identifier) @name.function
                  parameters: (parameters) @sig.function.params
                  return_type: (type)? @sig.function.return) @function))

;; Methods on classes (instance / static / classmethod / dunder all
;; surface as a single `function_definition` inside the class body —
;; the decorator distinguishes them at the runtime level, not at the
;; AST level).
(class_definition
  body: (block
          (function_definition
            name: (identifier) @name.method
            parameters: (parameters) @sig.method.params
            return_type: (type)? @sig.method.return) @method))

(class_definition
  body: (block
          (decorated_definition
            definition: (function_definition
                          name: (identifier) @name.method
                          parameters: (parameters) @sig.method.params
                          return_type: (type)? @sig.method.return) @method)))

;; Class declarations.
(class_definition
  name: (identifier) @name.class) @class

;; Imports.
(import_statement
  name: (dotted_name) @import.module) @import

(import_from_statement
  module_name: (dotted_name)? @import.module) @import.from

;; Call sites.
(call
  function: (identifier) @ref) @ref.call

(call
  function: (attribute
              attribute: (identifier) @ref)) @ref.method
