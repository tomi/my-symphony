# WORKFLOW.md schema

[`workflow.schema.json`](./workflow.schema.json) is a [JSON Schema](https://json-schema.org/)
(Draft 2020-12) for the **YAML front matter** of a `WORKFLOW.md` file. It is the
machine-readable form of the prose contract in [`SPEC.md` §5.3](../SPEC.md). The
Markdown body after the front matter is a Liquid prompt template and is not covered
by this schema.

Unknown keys are permitted (the runtime ignores them for forward compatibility, per
SPEC §5.3), so the schema does not forbid additional properties. Its value is
autocompletion, type/enum checking, and inline docs for the documented fields.

## Editor integration

Add a modeline as the first line of your front matter so the YAML language server
(e.g. the VS Code "YAML" extension) applies the schema:

```yaml
---
# yaml-language-server: $schema=schema/workflow.schema.json
tracker:
  kind: linear
  ...
---
```

Adjust the path to point at this file relative to your `WORKFLOW.md`.

## Programmatic validation

Any JSON Schema validator works after extracting the front matter to an object.
Example with Node (`ajv` + `js-yaml`):

```js
const Ajv = require('ajv/dist/2020');
const addFormats = require('ajv-formats');
const yaml = require('js-yaml');
const fs = require('fs');

const schema = JSON.parse(fs.readFileSync('schema/workflow.schema.json', 'utf8'));
const validate = addFormats(new Ajv()).compile(schema);

const md = fs.readFileSync('WORKFLOW.md', 'utf8');
const front = yaml.load(md.match(/^---\n([\s\S]*?)\n---/)[1]);
if (!validate(front)) console.error(validate.errors);
```

Note the schema documents canonical types (e.g. integers as `integer`); the daemon's
loader is more lenient at runtime (it also coerces numeric strings).
