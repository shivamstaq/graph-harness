from ariadne import QueryType, MutationType, SubscriptionType, gql, make_executable_schema


type_defs = gql("""
    type Query {
        user(id: ID!): User
        users: [User!]!
    }

    type Mutation {
        createUser(name: String!, age: Int): User!
    }

    type Subscription {
        userAdded: User
    }
""")

query = QueryType()
mutation = MutationType()
subscription = SubscriptionType()


@query.field("user")
def resolve_user(_, info, id):
    return find_user(id)


@query.field("users")
def resolve_users(_, info):
    return list_users()


def resolve_create_user(_, info, name, age=None):
    return create_user(name, age)


mutation.set_field("createUser", resolve_create_user)


@subscription.source("userAdded")
async def user_added_source(_, info):
    async for user in pubsub.subscribe("USER_ADDED"):
        yield user


schema = make_executable_schema(type_defs, query, mutation, subscription)
