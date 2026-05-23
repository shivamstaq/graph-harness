import strawberry
from typing import List, Optional


@strawberry.type
class User:
    id: strawberry.ID
    name: str


@strawberry.type
class Query:
    @strawberry.field
    def user(self, id: strawberry.ID) -> Optional[User]:
        return find_user(id)

    @strawberry.field
    def users(self) -> List[User]:
        return list_users()


@strawberry.type
class Mutation:
    @strawberry.mutation
    def create_user(self, name: str, age: Optional[int] = None) -> User:
        return create_user(name, age)


@strawberry.type
class Subscription:
    @strawberry.subscription
    async def user_added(self) -> User:
        async for user in pubsub.subscribe("USER_ADDED"):
            yield user
