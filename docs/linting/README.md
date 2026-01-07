# Documentation Linting

This directory contains configuration for documentation linting tools.

## Overview

We use multiple linters to ensure documentation quality:

1. **markdownlint-cli2** - Markdown syntax and style
2. **Vale** - Prose style and terminology
3. **markdown-link-check** - Link validation
4. **codespell** - Spell checking
5. **yamllint** - YAML linting

## Quick Start

### Install Tools

```bash
make docs-setup
```

This installs all required tools and sets up pre-commit hooks.

### Run Linters

```bash
# Run all linters
make docs-lint

# Run specific linters
make docs-lint-markdown
make docs-lint-prose
make docs-lint-links
make docs-lint-spell
make docs-lint-yaml
```

### Auto-fix Issues

```bash
make docs-fix
```

This automatically fixes common markdown issues.

## Configuration Files

### `.markdownlint.yaml`

Configures markdown syntax checking:
- Heading styles
- List formatting
- Line length (120 chars)
- Code block language tags
- Link formatting

See [markdownlint rules](https://github.com/DavidAnson/markdownlint/blob/main/doc/Rules.md) for details.

### `.vale.ini`

Configures prose linting:
- Writing style checks
- Terminology consistency
- Readability metrics
- Custom vocabulary

Vale checks against these style guides:
- **Vale**: General writing rules
- **Microsoft**: Microsoft Writing Style Guide
- **write-good**: Plain English recommendations

### `.vale/styles/Vocab/ClickHouse/`

Custom vocabulary for ClickHouse-specific terms:

- **accept.txt**: Accepted terms (won't be flagged)
- **reject.txt**: Rejected terms (suggests alternatives)

Add project-specific terms here to avoid false positives.

### `.pre-commit-config.yaml`

Pre-commit hooks that run automatically before commits:
- Markdown linting
- Trailing whitespace removal
- YAML validation
- Spell checking
- Link checking

Install with:
```bash
pip install pre-commit
pre-commit install
```

### `.yamllint.yaml`

YAML linting configuration:
- Indentation (2 spaces)
- Line length (120 chars)
- Comment formatting
- Trailing spaces

### `.markdown-link-check.json`

Link checker configuration:
- Timeout settings
- Retry behavior
- Ignored patterns (localhost, placeholders)

## CI Integration

GitHub Actions runs all linters on:
- Pull requests that modify documentation
- Pushes to main branch

See `.github/workflows/docs-lint.yaml` for details.

## Common Issues and Fixes

### Markdown Linting

**Issue**: `MD013 Line length`
```
docs/quickstart.md:45 MD013/line-length Line length [Expected: 120; Actual: 145]
```

**Fix**: Break long lines at 120 characters (except in code blocks and tables).

**Issue**: `MD040 Fenced code blocks should have a language`
```
docs/quickstart.md:23 MD040/fenced-code-language Fenced code blocks should have a language specified
```

**Fix**: Add language to code blocks:
````markdown
```bash
kubectl get pods
```
````

### Vale Prose Issues

**Issue**: `Vale.Spelling`
```
docs/configuration.md:15:23: Vale.Spelling Did you really mean 'Clickhouse'?
```

**Fix**: Use correct spelling `ClickHouse` or add to vocabulary if intentional.

**Issue**: `Microsoft.Passive`
```
docs/storage.md:42:1: Microsoft.Passive 'was created' may be passive voice.
```

**Fix**: Use active voice: "The operator creates..." instead of "Was created by..."

### Link Checking

**Issue**: `[✖] Broken link`
```
FILE: docs/quickstart.md
[✖] ./nonexistent.md → Status: 404
```

**Fix**: Update link to point to correct file or remove if no longer needed.

### Spell Checking

**Issue**: False positive on technical term

**Fix**: Add to `.vale/styles/Vocab/ClickHouse/accept.txt`:
```
StatefulSet
PersistentVolumeClaim
```

## Best Practices

### Writing Style

1. **Be concise**: Short sentences are easier to read
2. **Use active voice**: "The operator creates pods" not "Pods are created"
3. **Show examples**: Include working code samples
4. **Explain why**: Don't just show commands, explain purpose

### Markdown Formatting

1. **Use ATX headings**: `#` syntax, not underline style
2. **Consistent lists**: Use `-` for unordered lists
3. **Code language tags**: Always specify language in code blocks
4. **Descriptive links**: Use meaningful link text

### Maintaining Quality

1. **Run linters locally**: Before committing
2. **Fix issues promptly**: Don't accumulate linting debt
3. **Update vocabulary**: Add valid technical terms to accept list
4. **Review PR checks**: Ensure all checks pass

## Tools Documentation

- [markdownlint](https://github.com/DavidAnson/markdownlint)
- [Vale](https://vale.sh/)
- [markdown-link-check](https://github.com/tcort/markdown-link-check)
- [codespell](https://github.com/codespell-project/codespell)
- [yamllint](https://yamllint.readthedocs.io/)
- [pre-commit](https://pre-commit.com/)

## Contributing

See [CONTRIBUTING.md](../CONTRIBUTING.md) for documentation contribution guidelines.

## Support

Questions or issues with linting tools? Open a GitHub issue or discussion.
