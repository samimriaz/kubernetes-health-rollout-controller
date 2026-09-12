import os
import sys
import unittest
from pathlib import Path
from unittest.mock import patch


sys.path.insert(0, str(Path(__file__).parents[1]))

from app.main import current_mode, render_metrics


class MetricsTests(unittest.TestCase):
    def test_healthy_metrics_are_explicitly_simulated(self) -> None:
        metrics = render_metrics("healthy")

        self.assertIn('simulated_gpu_temperature_celsius{mode="healthy"} 58.0', metrics)
        self.assertIn('simulated_gpu_ecc_errors_total{mode="healthy"} 0.0', metrics)
        self.assertNotIn("DCGM_", metrics)

    def test_degraded_mode_exceeds_demo_thresholds(self) -> None:
        metrics = render_metrics("degraded")

        self.assertIn('simulated_gpu_temperature_celsius{mode="degraded"} 96.0', metrics)
        self.assertIn('simulated_gpu_ecc_errors_total{mode="degraded"} 8.0', metrics)

    def test_unknown_mode_falls_back_to_healthy(self) -> None:
        with patch.dict(os.environ, {"SIMULATED_GPU_MODE": "unknown"}):
            self.assertEqual(current_mode(), "healthy")


if __name__ == "__main__":
    unittest.main()