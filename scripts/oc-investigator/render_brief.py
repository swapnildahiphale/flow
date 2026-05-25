#!/usr/bin/env python3
"""Render the OC investigator brief from a template + vars dict.

Uses string.Template.safe_substitute() so special characters in ticket
fields (quotes, backticks, newlines) don't break rendering.
"""
import argparse
import json
import string
import sys

MAX_DESCRIPTION_CHARS = 2000


def truncate_description(text, ticket_url: str) -> str:
    """Truncate Jira description to MAX_DESCRIPTION_CHARS, appending a pointer."""
    if not text:
        return ""
    if len(text) <= MAX_DESCRIPTION_CHARS:
        return text
    head = text[:MAX_DESCRIPTION_CHARS]
    return f"{head}\n\n... (truncated — see {ticket_url})"


def render(template_path: str, vars: dict) -> str:
    with open(template_path, "r") as f:
        tmpl = string.Template(f.read())
    return tmpl.safe_substitute(vars)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Render OC investigator brief from template + JSON vars."
    )
    parser.add_argument("--template", required=True, help="Path to brief template")
    parser.add_argument(
        "--vars",
        required=True,
        help="JSON string of vars dict, e.g. '{\"TICKET_KEY\":\"OC-1\"}'",
    )
    args = parser.parse_args()
    vars_dict = json.loads(args.vars)
    sys.stdout.write(render(args.template, vars_dict))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
