import graphene


class User(graphene.ObjectType):
    id = graphene.ID(required=True)
    name = graphene.String(required=True)


class Query(graphene.ObjectType):
    user = graphene.Field(User, id=graphene.ID(required=True))
    users = graphene.List(graphene.NonNull(User))

    def resolve_user(self, info, id):
        return find_user(id)

    def resolve_users(self, info):
        return list_users()


class CreateUser(graphene.Mutation):
    class Arguments:
        name = graphene.String(required=True)
        age = graphene.Int(required=False)

    user = graphene.Field(User)

    def mutate(self, info, name, age=None):
        return CreateUser(user=User(name=name))


class Mutation(graphene.ObjectType):
    create_user = CreateUser.Field()
