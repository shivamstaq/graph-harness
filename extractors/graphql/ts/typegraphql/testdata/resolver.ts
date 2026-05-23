import { Resolver, Query, Mutation, Subscription, Arg } from 'type-graphql';

@Resolver(() => User)
export class UserResolver {
  @Query(() => User, { nullable: true })
  async user(@Arg('id') id: string): Promise<User | null> {
    return findUser(id);
  }

  @Query(() => [User])
  async users(): Promise<User[]> {
    return listUsers();
  }

  @Mutation(() => User)
  async createUser(@Arg('name') name: string, @Arg('age', { nullable: true }) age?: number): Promise<User> {
    return createUser(name, age);
  }

  @Subscription({ topics: 'USER_ADDED' })
  userAdded(): User {
    return null as any;
  }
}
