import io
import unittest
from contextlib import redirect_stdout

from chill_loadgen.cli import main


class CLITest(unittest.TestCase):
    def test_empty_invocation_prints_help(self) -> None:
        output = io.StringIO()

        with redirect_stdout(output):
            exit_code = main([])

        self.assertEqual(exit_code, 0)
        self.assertIn("open-loop inference workload", output.getvalue())


if __name__ == "__main__":
    unittest.main()
