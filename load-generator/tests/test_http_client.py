import unittest
from unittest.mock import Mock, patch

from chill_loadgen.http_client import InferenceResponse, KServeV2HTTPClient


class KServeV2HTTPClientTest(unittest.TestCase):
    @patch("chill_loadgen.http_client.HTTPConnection")
    def test_posts_the_original_batch_to_the_chill_inference_path(
        self, connection_type: Mock
    ) -> None:
        connection = connection_type.return_value
        response = connection.getresponse.return_value
        response.status = 200
        response.read.return_value = b'{"outputs":[]}'
        body = b'{"inputs":[{"shape":[2,3],"data":[1,2,3,4,5,6]}]}'

        client = KServeV2HTTPClient(
            "http://chill.example:8080/gateway/", timeout_seconds=5.0
        )
        result = client.infer("mobilenet-v2-050", body)

        connection_type.assert_called_once_with(
            "chill.example", 8080, timeout=5.0
        )
        connection.request.assert_called_once_with(
            "POST",
            "/gateway/v2/models/mobilenet-v2-050/infer",
            body=body,
            headers={"Content-Type": "application/json"},
        )
        self.assertEqual(InferenceResponse(200, b'{"outputs":[]}'), result)


if __name__ == "__main__":
    unittest.main()
