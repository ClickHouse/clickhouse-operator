# Documentation Linting Setup Summary

This document summarizes the documentation linting infrastructure that has been set up for the ClickHouse Operator project.

## What Was Implemented

### 1. Linting Tools Configuration

#### markdownlint-cli2 (`.markdownlint.yaml`)
- Markdown syntax and style checking
- 120 character line length limit
- ATX-style headings (`#` syntax)
- Code blocks require language tags
- Consistent list formatting

#### Vale (`.vale.ini`)
- Prose style checking
- Uses Microsoft Writing Style Guide
- Custom vocabulary for technical terms
- Checks for passive voice, complex sentences, unclear language

#### Custom Vocabulary (`.vale/styles/Vocab/ClickHouse/`)
- `accept.txt`: Approved technical terms (ClickHouse, Kubernetes, kubectl, etc.)
- `reject.txt`: Terms to avoid (incorrect spellings, deprecated terms)

#### markdown-link-check (`.markdown-link-check.json`)
- Validates all markdown links
- Checks internal and external links
- Configurable timeouts and retries

#### codespell
- Spell checking for documentation
- Configured to skip code files and dependencies
- Custom ignore list for technical terms

#### yamllint (`.yamllint.yaml`)
- YAML file validation
- 2-space indentation
- 120 character line length
- Comment and formatting rules

### 2. Automation

#### Pre-commit Hooks (`.pre-commit-config.yaml`)
Automatically runs before each commit:
- Markdown linting
- Trailing whitespace removal
- YAML validation
- Spell checking
- Link checking
- Mixed line ending fixes

Install with:
```bash
pip install pre-commit
pre-commit install
```

#### CI/CD Workflow (`.github/workflows/docs-lint.yaml`)
Runs on:
- Pull requests modifying documentation
- Pushes to main branch

Checks:
- Markdown syntax
- Prose style
- Link validity
- Spelling
- YAML formatting
- Internal link consistency
- Example references

#### Local Linting Script (`hack/lint-docs.sh`)
Convenience script for local development:
```bash
./hack/lint-docs.sh           # Run all checks
./hack/lint-docs.sh markdown  # Run only markdown checks
./hack/lint-docs.sh prose     # Run only Vale
./hack/lint-docs.sh links     # Run only link checking
./hack/lint-docs.sh spell     # Run only spell checking
./hack/lint-docs.sh yaml      # Run only YAML linting
```

#### Makefile Targets
Added to project Makefile:
```bash
make docs-setup         # Install all linting tools
make docs-lint          # Run all linters
make docs-lint-markdown # Lint markdown only
make docs-lint-prose    # Lint prose only
make docs-lint-links    # Check links only
make docs-lint-spell    # Spell check only
make docs-lint-yaml     # Lint YAML only
make docs-fix           # Auto-fix issues
```

### 3. Documentation

#### Contributing Guide (`docs/CONTRIBUTING.md`)
Comprehensive guide covering:
- Documentation structure
- Writing guidelines
- Markdown style
- Code examples best practices
- Setting up development environment
- Linting documentation locally
- Pull request process
- Style guide and terminology

#### Linting README (`docs/linting/README.md`)
Quick reference for:
- Overview of linting tools
- Configuration files
- Common issues and fixes
- Best practices
- CI integration

#### PR Template (`.github/PULL_REQUEST_TEMPLATE.md`)
Includes documentation checklist:
- Documentation linting verification
- Link checking
- Spell check confirmation
- Code example testing

## Directory Structure

```
clickhouse-operator/
├── .markdownlint.yaml              # Markdown linting config
├── .vale.ini                       # Prose linting config
├── .vale/
│   └── styles/
│       └── Vocab/
│           └── ClickHouse/
│               ├── accept.txt      # Accepted terms
│               └── reject.txt      # Rejected terms
├── .yamllint.yaml                  # YAML linting config
├── .markdown-link-check.json       # Link checking config
├── .pre-commit-config.yaml         # Pre-commit hooks config
├── .github/
│   ├── workflows/
│   │   └── docs-lint.yaml          # CI workflow for docs
│   └── PULL_REQUEST_TEMPLATE.md    # PR template
├── hack/
│   └── lint-docs.sh                # Local linting script
├── docs/
│   ├── CONTRIBUTING.md             # Contribution guide
│   └── linting/
│       ├── README.md               # Linting overview
│       └── SETUP.md                # This file
└── Makefile                        # Added docs targets
```

## Getting Started

### For Contributors

1. **Install tools**:
   ```bash
   make docs-setup
   ```

2. **Write documentation** following guidelines in `docs/CONTRIBUTING.md`

3. **Run linters locally**:
   ```bash
   make docs-lint
   ```

4. **Auto-fix issues**:
   ```bash
   make docs-fix
   ```

5. **Commit changes** (pre-commit hooks run automatically)

### For Maintainers

1. **Review PR checks**: Ensure all documentation linting passes
2. **Enforce standards**: Request fixes for linting failures
3. **Update vocabulary**: Add valid terms to `accept.txt` as needed
4. **Monitor CI**: Check workflow runs in GitHub Actions

## Linting Rules Summary

### Markdown Rules

- ✅ Use ATX-style headings (`#`, `##`, `###`)
- ✅ Specify language in code blocks
- ✅ Keep lines under 120 characters (except code/tables)
- ✅ Use `-` for unordered lists
- ✅ Use `1. 2. 3.` for ordered lists
- ✅ One top-level heading per file
- ✅ No trailing punctuation in headings
- ✅ No bare URLs (use link syntax)

### Prose Rules

- ✅ Use active voice
- ✅ Keep sentences concise
- ✅ Use correct terminology
- ✅ Avoid passive constructions
- ✅ Write in plain English
- ✅ Be inclusive and respectful

### YAML Rules

- ✅ 2-space indentation
- ✅ No trailing spaces
- ✅ Consistent key-value formatting
- ✅ Maximum 120 character lines
- ✅ Comments have space after `#`

## Benefits

### Quality
- Consistent documentation style
- Fewer errors and typos
- Professional appearance
- Better readability

### Developer Experience
- Clear contribution guidelines
- Automated checks catch issues early
- Quick feedback loop
- Less manual review needed

### Maintainability
- Easier to maintain docs long-term
- Standards enforced automatically
- Scales as project grows
- Reduces technical debt

## Customization

### Add Accepted Terms

Edit `.vale/styles/Vocab/ClickHouse/accept.txt`:
```
MyNewTerm
AnotherTechnicalTerm
```

### Adjust Line Length

Edit `.markdownlint.yaml`:
```yaml
MD013:
  line_length: 150  # Change from 120
```

### Skip Specific Rules

Disable rules in `.markdownlint.yaml`:
```yaml
MD013: false  # Disable line length check
```

### Update Vale Styles

Edit `.vale.ini` to add/remove style packages:
```ini
BasedOnStyles = Vale, Microsoft, write-good, Google
```

## Troubleshooting

### Tools Not Found

Install missing tools:
```bash
make docs-setup
```

Or install individually:
```bash
npm install -g markdownlint-cli2 markdown-link-check
pip install vale codespell yamllint pre-commit
```

### Vale Styles Missing

Vale downloads styles on first run. If missing:
```bash
vale sync
```

### Pre-commit Not Running

Reinstall hooks:
```bash
pre-commit uninstall
pre-commit install
```

### False Positives

Add terms to vocabulary:
```bash
echo "MyTerm" >> .vale/styles/Vocab/ClickHouse/accept.txt
```

## Future Improvements

Potential enhancements:
- [ ] Add automated link rot detection
- [ ] Integrate with documentation site builder
- [ ] Add accessibility checks
- [ ] Include diagram validation
- [ ] Add API documentation linting
- [ ] Create documentation metrics dashboard

## Resources

- [Markdown Guide](https://www.markdownguide.org/)
- [Vale Documentation](https://vale.sh/docs/)
- [Microsoft Writing Style Guide](https://learn.microsoft.com/en-us/style-guide/welcome/)
- [Kubernetes Documentation Style](https://kubernetes.io/docs/contribute/style/style-guide/)

## Support

Questions or issues? Open a GitHub issue or check `docs/CONTRIBUTING.md`.
