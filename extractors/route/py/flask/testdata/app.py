"""Flask fixture for the routes.py.flask extractor tests."""

from flask import Blueprint, Flask

app = Flask(__name__)
bp = Blueprint("api", __name__, url_prefix="/api")


@app.route("/health")
def health():
    return "ok"


@app.route("/login", methods=["POST"])
def login():
    return {"token": "xxx"}


@app.route("/orders", methods=["GET", "POST"])
def orders():
    return []


@bp.route("/users/<int:user_id>", methods=["GET"])
def get_user(user_id):
    return {"id": user_id}


@bp.route("/users/<int:user_id>", methods=["PUT", "PATCH"])
def update_user(user_id):
    return {"id": user_id}
