"""Unit tests for render_brief — uses stdlib unittest, no extra deps."""
import os
import tempfile
import unittest

# render_brief.py is in the same directory; import directly.
import sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from render_brief import render, MAX_DESCRIPTION_CHARS


def _write_template(body: str) -> str:
    fd, path = tempfile.mkstemp(suffix=".md")
    with os.fdopen(fd, "w") as f:
        f.write(body)
    return path


class TestBasicSubstitution(unittest.TestCase):
    def test_substitutes_known_placeholders(self):
        tmpl = _write_template("Hello $NAME — ticket $TICKET_KEY")
        out = render(tmpl, {"NAME": "Swapnil", "TICKET_KEY": "OC-1905"})
        self.assertEqual(out, "Hello Swapnil — ticket OC-1905")

    def test_leaves_unknown_placeholders_intact(self):
        # safe_substitute does NOT raise on missing keys; the literal $X stays.
        tmpl = _write_template("Hello $NAME, $UNKNOWN")
        out = render(tmpl, {"NAME": "Swapnil"})
        self.assertEqual(out, "Hello Swapnil, $UNKNOWN")


class TestSpecialCharSafety(unittest.TestCase):
    def test_handles_quotes_and_backticks(self):
        tmpl = _write_template("Summary: $SUMMARY")
        nasty = 'Pod "foo" failed `echo $PATH` — ${var} && rm -rf /'
        out = render(tmpl, {"SUMMARY": nasty})
        self.assertEqual(out, f"Summary: {nasty}")

    def test_handles_newlines_in_value(self):
        tmpl = _write_template("Desc:\n$DESC")
        multi = "line one\nline two\nline three"
        out = render(tmpl, {"DESC": multi})
        self.assertEqual(out, f"Desc:\n{multi}")


class TestDescriptionTruncation(unittest.TestCase):
    def test_truncates_long_description_with_pointer(self):
        from render_brief import truncate_description

        long = "x" * 3000
        url = "https://jira.getinsured.com/browse/OC-1"
        out = truncate_description(long, url)
        self.assertLess(len(out), 3000)
        self.assertIn("(truncated", out)
        self.assertIn(url, out)
        # First 2000 chars are preserved verbatim
        self.assertTrue(out.startswith("x" * MAX_DESCRIPTION_CHARS))

    def test_short_description_unchanged(self):
        from render_brief import truncate_description

        short = "short description"
        out = truncate_description(short, "https://example.com/OC-1")
        self.assertEqual(out, short)

    def test_handles_none_description(self):
        from render_brief import truncate_description

        out = truncate_description(None, "https://example.com/OC-1")
        self.assertEqual(out, "")


if __name__ == "__main__":
    unittest.main()
