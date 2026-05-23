"""Django URL conf fixture for the routes.py.django extractor tests."""

from django.urls import include, path, re_path
from django.views.decorators.http import require_GET, require_http_methods

from . import views


@require_GET
def health(request):
    return None


@require_http_methods(["POST", "PUT"])
def save_thing(request):
    return None


urlpatterns = [
    path("health/", health, name="health"),
    path("things/", save_thing, name="save-thing"),
    path("orders/", views.orders_index, name="orders-index"),
    path("orders/<int:pk>/", views.order_detail, name="order-detail"),
    re_path(r"^legacy/(?P<slug>[\w-]+)/$", views.legacy_view, name="legacy"),
    path("api/", include("myapp.api.urls")),
    path("admin/", views.AdminPanel.as_view(), name="admin"),
]
