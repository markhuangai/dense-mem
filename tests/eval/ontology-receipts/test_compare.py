import unittest

from compare import pair


class PairTests(unittest.TestCase):
    def test_three_slower_corrections_block(self):
        self.assertTrue(pair([100] * 4, [130, 130, 130, 90])["repeatable_regression"])
        self.assertTrue(pair([100] * 4, [130, 130, 130, 100])["repeatable_regression"])

    def test_one_regression_does_not_repeat(self):
        self.assertFalse(pair([100] * 4, [80, 90, 100, 160])["repeatable_regression"])

    def test_primary_requires_four_wins_above_dispersion(self):
        self.assertTrue(pair([100] * 4, [85, 90, 95, 85])["passed"])
        self.assertFalse(pair([100] * 4, [85, 90, 95, 100])["passed"])
        self.assertFalse(pair([80, 100, 120, 140], [79, 99, 119, 139])["passed"])


if __name__ == "__main__":
    unittest.main()
