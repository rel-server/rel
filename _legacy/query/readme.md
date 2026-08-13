Informal grammar:

```
toplevel: full_operation ("|" full_operation)*

full_operation:
  ("call" | "get" | "update" | "delete" | "insert" | "upsert" | "merge")?
  sql_identifer
  selection

selection:
	("(" select_clause ")")?
	where_clause?
	order_clause?
	limit_clause?

select_clause:
	| "*" ("-" identifier)*
	| field
  | "$$merge_operation"
  | relationship

field: identifier (":" expression)?

relationship:
  | "@" identifier selection
  | identifier ":" "@" identifier selection

sql_identifier: identifier "." identifier

order_clause:
  "order" order_member ("," order_member)*

order_member:
  expression ("desc" | "asc")?

expression:
  | identifier
	| number
	| string
	| unaryop expression
	| expression binop expression
	| allowed_function "(" expression ("," expression)* ")"
	| "(" expression (("," expression)+ ","?)? ")"
  | "[" expression ("," expression)* "]"
  | expression "[" expression "]"
	| expression "::" sql_identifier

binaryop: "-" | "*" | "/" | "+" | "->" | "->>" | "||"

unaryop: "-" | "+" | "not" | "!"

allowed_function:
  -- not sure
  | "coalesce"
  | "count"
  | "avg"
  | "sum"
```

Expected bodies:
----------------

When batching several operations, the toplevel provided json object must be an array that supplies the arguments sequentially to all the operations

- *call* : an object which keys are the function argument's names
- *update* : *one* object whose keys / values shall be used for the update statement
- *get* | *delete* : nothing or null if in a multiple request
- *insert* | *upsert* | *merge* : the list of objects that will be changed in the database.

Example queries:
----------------

```
api.users (
  $: * - some_field,
  $manager: @manager,
  $managees: @users_by_manager,
  $own_bilans: @bilans_by_username { * - signatures },
)
where id = 1
```

```
api.bilan (
  $: *,
  @manager
)
```