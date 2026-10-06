import unittest
from pathlib import Path
from value import value
from shared import shared
SEED = 7
class FixtureTests(unittest.TestCase):
    def setUp(self):
        self.assertEqual(SEED, 7)
    def test_direct(self):
        self.assertEqual(value(), 7)
    def test_shared(self):
        self.assertEqual(shared(), 14)
    def test_resource(self):
        self.assertEqual(Path("data/value.txt").read_text(), "ready\n")
