"""Exercise the privacy scanner against temporary Git histories."""

from pathlib import Path
import subprocess
import tempfile
import unittest


SCANNER = Path(__file__).with_name("check-public-safety.sh").resolve()


def login_url(authority):
    return "https://" + authority


class PrivacyScannerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "Test fixture")
        self.git("config", "user.email", "fixture@example.invalid")

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args],
                              check=True, capture_output=True, text=True)

    def commit(self, text):
        if isinstance(text, bytes):
            (self.repo / "source.py").write_bytes(text)
        else:
            (self.repo / "source.py").write_text(text + "\n")
        self.git("add", "source.py")
        self.git("commit", "-q", "-m", "test: add scanner fixture")

    def scan(self, history=False):
        return subprocess.run([str(SCANNER), *(["--history"] if history else []), str(self.repo)],
                              capture_output=True, text=True, errors="replace")

    def test_source_method_and_exact_fake_login_are_allowed_in_history(self):
        self.commit("root = Path.home(); url = 'https://user:password@example.com'")
        self.commit("root = 'removed fixtures'")
        result = self.scan(history=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_fake_login_does_not_allow_other_hosts_or_credentials(self):
        for url in (login_url("user:password@example.com.evil"),
                    login_url("user:real-password@example.com"),
                    login_url("user:password@other.example")):
            with self.subTest(url=url):
                self.commit("url = " + repr(url))
                self.assertNotEqual(self.scan().returncode, 0)

    def test_fixture_does_not_hide_sensitive_content_on_same_line(self):
        address = ".".join(("10", "12", "34", "56"))
        host = "machine" + ".home"
        for sensitive in (address, host, "/Users/" + "fixture/private"):
            with self.subTest(sensitive=sensitive):
                self.commit("root = Path.home(); url = 'https://user:password@example.com'; value = " + repr(sensitive))
                self.assertNotEqual(self.scan().returncode, 0)

    def test_deleted_sensitive_content_still_fails_history_scan(self):
        self.commit("url = " + repr(login_url("fixture:secret@other.example")))
        self.commit("url = 'https://example.com'")
        self.assertEqual(self.scan().returncode, 0)
        self.assertNotEqual(self.scan(history=True).returncode, 0)

    def test_sensitive_hit_at_start_of_large_line_is_not_dropped(self):
        token = "ghp_" + "x" * 35
        self.commit("root = Path.home(); value = " + repr(token) + "; padding = '" + "z" * 200_000 + "'")
        self.assertNotEqual(self.scan().returncode, 0)
        self.commit("root = 'removed'")
        self.assertNotEqual(self.scan(history=True).returncode, 0)

    def test_non_utf8_text_does_not_hide_token(self):
        token = ("ghp_" + "x" * 35).encode()
        self.commit(b"value = '" + token + b"'; extra = '\xe9'\n")
        self.assertNotEqual(self.scan().returncode, 0)

    def test_invalid_private_rule_fails_without_printing_rule(self):
        self.commit("value = 'ordinary source'")
        private_rule = "(" + "private-fixture-pattern"
        (self.repo / ".public-safety-denylist").write_text(private_rule + "\n")
        result = self.scan()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(private_rule, result.stderr)

    def test_operator_denylist_still_matches_original_fixture_content(self):
        self.commit("root = Path.home(); url = 'https://user:password@example.com'")
        for pattern in (r"Path\.home", r"user:password"):
            with self.subTest(pattern=pattern):
                (self.repo / ".public-safety-denylist").write_text(pattern + "\n")
                self.assertNotEqual(self.scan(history=True).returncode, 0)

    def test_fixture_does_not_hide_private_key_or_provider_token(self):
        key = "-----BEGIN " + "PRIVATE KEY-----"
        token = "ghp_" + "x" * 35
        for sensitive in (key, token):
            with self.subTest(sensitive=sensitive):
                self.commit("root = Path.home(); value = " + repr(sensitive))
                self.assertNotEqual(self.scan().returncode, 0)


if __name__ == "__main__":
    unittest.main()
