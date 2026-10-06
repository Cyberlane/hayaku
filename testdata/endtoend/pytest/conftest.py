import pytest
@pytest.fixture(autouse=True)
def shared_fixture():
    assert 7 == 7
