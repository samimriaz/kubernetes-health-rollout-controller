"""MobileNetV2 image preprocessing and ONNX inference.

An ONNX file stores a trained model's graph and learned weights in a portable
format. ONNX Runtime loads that file and executes it without requiring the
training framework that originally produced the model.
"""

from io import BytesIO
from pathlib import Path

import numpy as np
import onnxruntime as ort
from PIL import Image, ImageOps, UnidentifiedImageError

# MobileNetV2 was trained with 224x224 RGB images normalized using these
# ImageNet channel statistics. Inference inputs must use the same preparation.
IMAGE_SIZE = (224, 224)
IMAGENET_MEAN = np.array([0.485, 0.456, 0.406], dtype=np.float32)
IMAGENET_STD = np.array([0.229, 0.224, 0.225], dtype=np.float32)


class InvalidImageError(ValueError):
    """Raised when uploaded bytes cannot be decoded as an image."""


class MobileNetClassifier:
    """Load MobileNetV2 once and reuse it for every prediction request."""

    def __init__(self, model_path: Path) -> None:
        # CPUExecutionProvider makes the guaranteed local path independent of a
        # GPU. A GPU provider can be selected in a later project increment.
        self.session = ort.InferenceSession(
            model_path,
            providers=["CPUExecutionProvider"],
        )
        self.input_name = self.session.get_inputs()[0].name

    def predict(self, image_bytes: bytes) -> dict[str, int | float]:
        """Return the most likely ImageNet class index and confidence."""
        input_tensor = self._preprocess(image_bytes)
        output_scores = self.session.run(None, {self.input_name: input_tensor})[0][0]

        # Softmax converts the model's raw scores into probabilities that sum
        # to 1.0. Subtracting the maximum prevents numeric overflow in exp().
        exponentials = np.exp(output_scores - np.max(output_scores))
        probabilities = exponentials / exponentials.sum()
        class_index = int(np.argmax(probabilities))

        return {
            "class_index": class_index,
            "confidence": float(probabilities[class_index]),
        }

    @staticmethod
    def _preprocess(image_bytes: bytes) -> np.ndarray:
        """Decode uploaded bytes and create a [1, 3, 224, 224] float tensor."""
        try:
            with Image.open(BytesIO(image_bytes)) as image:
                # ImageOps.fit resizes and center-crops while preserving aspect
                # ratio. RGB guarantees exactly three color channels.
                image = ImageOps.fit(image.convert("RGB"), IMAGE_SIZE)
                pixels = np.asarray(image, dtype=np.float32) / 255.0
        except (UnidentifiedImageError, OSError) as error:
            raise InvalidImageError("The uploaded file is not a valid image.") from error

        normalized = (pixels - IMAGENET_MEAN) / IMAGENET_STD
        # Pillow returns [height, width, channels]. MobileNet expects channels
        # first, plus an outer batch dimension: [1, 3, 224, 224].
        channels_first = normalized.transpose(2, 0, 1)
        return np.expand_dims(channels_first, axis=0).astype(np.float32)
