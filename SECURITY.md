# Security Policy

## Supported Versions

| Version        | Supported          |
| -------------- | ------------------ |
| main           | :white_check_mark: |
| Latest release | :white_check_mark: |
| Older releases | :x:                |

## Reporting a Vulnerability

We appreciate responsible disclosure of security vulnerabilities.

### Do Not Create Public Issues

**Please do not report security vulnerabilities through public GitHub issues.** Public disclosure before a fix is available can put users at risk.

### Report Privately

Send an email to: **[security@altairalabs.ai](mailto:security@altairalabs.ai)**

Include the following information:

- **Description** of the vulnerability
- **Steps to reproduce** the issue
- **Potential impact** and attack scenarios
- **Suggested fixes** or mitigations, if any
- Your **contact information** for follow-up

### Response Timeline

- **Initial Response**: Within 48 hours
- **Triage**: Within 5 business days
- **Resolution**: Typically within 30-90 days depending on severity

## Security Measures

### Static Analysis

- **gosec** via golangci-lint runs on all pull requests to catch common Go security issues.

### Dependency Management

- **Dependabot** monitors Go module dependencies and creates pull requests for security updates automatically.

### Code Review

- All changes require peer review before merging to `main`.

## Security Considerations for Users

### API Token Handling

This adapter interacts with the Omnia Management API. Follow these practices:

- **Never commit API tokens** to version control.
- Use the **OMNIA_API_TOKEN environment variable** rather than the `api_token` config field where possible.
- Apply the **principle of least privilege** when creating API tokens.
- Rotate tokens regularly.

### Input Validation

- All configuration inputs are validated before use. Ensure configuration files are sourced from trusted locations.

## Resources

- **Security Advisories**: [GitHub Security Advisories](https://github.com/AltairaLabs/PromptArena-deploy-omnia/security/advisories)
- **Security Contact**: [security@altairalabs.ai](mailto:security@altairalabs.ai)
- **Parent Project**: This adapter is part of the [PromptKit](https://github.com/AltairaLabs/PromptKit) ecosystem.

---

**Last Updated**: March 18, 2026

For questions about this security policy, contact: [security@altairalabs.ai](mailto:security@altairalabs.ai)
